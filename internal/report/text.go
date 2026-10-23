package report

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// RenderText writes the human-readable report to w.
//
// The report leads with the conclusion — a reader who stops after the first
// line must still know whether there is something to act on. It then lists
// every finding, worst severity first, each with enough context to go and
// fix it, and closes with the tallies.
//
// The output is deterministic: the same report always renders to the same
// bytes.
func RenderText(w io.Writer, r *core.Report) error {
	if w == nil {
		return errors.New("report: nil writer")
	}
	if r == nil {
		return errors.New("report: nil report")
	}
	bw := bufio.NewWriter(w)
	if err := renderText(bw, r); err != nil {
		return err
	}
	return bw.Flush()
}

// renderText does the actual writing. The caller owns the buffering.
func renderText(w io.Writer, r *core.Report) error {
	findings := sortedFindings(r)

	if err := writeLine(w, headline(r, findings)); err != nil {
		return err
	}
	if r.Project != "" {
		if err := writeLine(w, "项目: "+r.Project); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, strings.Repeat("-", ruleWidth)); err != nil {
		return err
	}

	for i, f := range findings {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if err := writeFinding(w, f); err != nil {
			return err
		}
	}

	if len(findings) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, strings.Repeat("-", ruleWidth)); err != nil {
			return err
		}
	}

	if err := writeField(w, "统计", fmt.Sprintf(
		"proven %d · vacuous %d · 无测试 %d · 无法判定 %d · 不适用 %d · 审不了 %d",
		r.Proven, r.Vacuous, r.NoTest, r.Inconclusive, r.NotApplicable, r.AuditFailed)); err != nil {
		return err
	}
	// The silence gets its own line rather than another number in the
	// tally.
	//
	// It is not a verdict, so putting it in the tally would make the
	// tally's partition untrue; and the tally already sits at the width
	// budget, so a seventh number would split across two lines. A line of
	// its own also says more than a number can: the reader learns that a
	// commit was judged, that the finding was suppressed on purpose, and
	// that the reason is written next to the code.
	if r.Silenced > 0 {
		if err := writeField(w, "豁免", fmt.Sprintf(
			"%d 个提交判出了结论，但发现被 // vacuous:ignore 注释压住 —— "+
				"注释就写在触发它的代码旁边", r.Silenced)); err != nil {
			return err
		}
	}
	return writeField(w, "按类别", byKindLine(r))
}

// writeField writes a "label: value" line, wrapped to the width budget.
// Continuation lines are indented under the first one, so that a long
// footer stays readable instead of running off the page.
func writeField(w io.Writer, label, value string) error {
	return writeLine(w, label+": "+value)
}

// writeLine writes text at the left margin, wrapped to the width budget.
// Continuation lines are indented, so a wrapped line still reads as one
// unit.
func writeLine(w io.Writer, text string) error {
	lines := wrapText(text, maxWidth, maxWidth-len(bodyIndent))
	for i, line := range lines {
		if i == 0 {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", bodyIndent, line); err != nil {
			return err
		}
	}
	return nil
}

// headline is the first line of the report: the conclusion.
//
// A clean run says so in as many words. A dirty run says how bad it is,
// so that the reader can decide whether to read the rest.
func headline(r *core.Report, findings []core.Finding) string {
	// How much of the run was actually judged.
	//
	// "No findings" and "nothing was judged" must not read the same. A
	// window of twenty merge commits produces no findings and no verdicts,
	// and a headline that says "no vacuous tests found" about it is
	// exactly the confusion this tool exists to catch - in itself.
	judged := r.Proven + r.Vacuous + r.NoTest + r.Inconclusive
	unjudged := r.NotApplicable + r.AuditFailed

	if len(findings) == 0 {
		switch {
		case r.AuditFailed > 0:
			return fmt.Sprintf("vacuous — 有 %d 个提交没能审成，报告不完整", r.AuditFailed)
		case judged == 0:
			return fmt.Sprintf("vacuous — 这一轮没有可判的提交（%d 个不适用）", r.NotApplicable)
		case r.Proven > 0:
			return fmt.Sprintf("vacuous — %d 个修复都有测试守住", r.Proven)
		default:
			return "vacuous — 没有发现问题"
		}
	}

	var critical, high int
	for _, f := range findings {
		switch f.Severity {
		case core.SevCritical:
			critical++
		case core.SevHigh:
			high++
		}
	}
	// The scope of the run belongs in the first line: a reader who does
	// not know how much was judged cannot weigh what was found.
	scope := fmt.Sprintf("审了 %d 个提交", judged)
	if unjudged > 0 {
		scope += fmt.Sprintf("，%d 个不适用", r.NotApplicable)
	}
	if r.AuditFailed > 0 {
		scope += fmt.Sprintf("，%d 个没审成", r.AuditFailed)
	}
	return fmt.Sprintf("vacuous — 发现 %d 个问题（%s %d · %s %d · 没测试看着 %d；%s）",
		len(findings), severityLabel(core.SevCritical), critical,
		severityLabel(core.SevHigh), high, r.Vacuous, scope)
}

// writeFinding writes one finding: the header line, then the prose.
func writeFinding(w io.Writer, f core.Finding) error {
	loc := f.Location()
	if loc == "" {
		loc = "(仓库根)"
	}
	head := fmt.Sprintf("  %-10s %-*s %s", severityTag(f.Severity), kindWidth, string(f.Kind), loc)
	if _, err := fmt.Fprintln(w, strings.TrimRight(head, " ")); err != nil {
		return err
	}

	if err := writeWrapped(w, f.Summary); err != nil {
		return err
	}
	if f.Detail != "" && f.Detail != f.Summary {
		if err := writeWrapped(w, "说明: "+f.Detail); err != nil {
			return err
		}
	}
	if f.Evidence != "" {
		if err := writeEvidence(w, f.Evidence); err != nil {
			return err
		}
	}
	if f.Hint != "" {
		if err := writeWrapped(w, "怎么修: "+f.Hint); err != nil {
			return err
		}
	}
	if f.Repro != "" {
		if err := writeRepro(w, f.Repro); err != nil {
			return err
		}
	}
	return nil
}

// writeWrapped writes text to the body column, wrapped to the width budget.
func writeWrapped(w io.Writer, text string) error {
	for _, line := range wrapText(text, maxWidth-len(bodyIndent), maxWidth-len(bodyIndent)) {
		if line == "" {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", bodyIndent, line); err != nil {
			return err
		}
	}
	return nil
}

// writeRepro writes the commands that reproduce a finding by hand.
//
// The block is deliberately copy-pasteable and deliberately unformatted: it
// is something to run, not something to read. A reader who does not trust
// the tool runs these and sees the same output the tool saw.
func writeRepro(w io.Writer, repro string) error {
	if err := writeWrapped(w, "怎么自己验一遍:"); err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimRight(repro, "\n"), "\n") {
		if _, err := fmt.Fprintf(w, "      %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// writeEvidence writes raw evidence.
//
// Evidence is quoted output: it is indented and labelled, but never
// re-wrapped, because a re-wrapped command output is not the output
// anymore.
func writeEvidence(w io.Writer, evidence string) error {
	lines := strings.Split(strings.TrimRight(evidence, "\n"), "\n")
	for i, line := range lines {
		if i == 0 {
			if _, err := fmt.Fprintf(w, "%s证据: %s\n", bodyIndent, line); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", bodyIndent, line); err != nil {
			return err
		}
	}
	return nil
}

// byKindLine is the "按类别" footer: how many findings of each kind.
//
// Kinds are listed in name order, not count order: a stable order keeps the
// footer diff-friendly across runs.
func byKindLine(r *core.Report) string {
	counts := r.ByKind()
	if len(counts) == 0 {
		return "无"
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[core.Kind(k)]))
	}
	return strings.Join(parts, " · ")
}

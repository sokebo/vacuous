package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// ---------------------------------------------------------------------------
// 负对照 (negative control)
//
// 这些测试只有在渲染逻辑真的坏了的时候才会变红。交付前做过一次真实的负对照：
// 逐个把渲染逻辑改坏，跑测试确认变红，然后还原。五次破坏全部确认过：
//
//  1. 把 sortedFindings 里的 core.SortFindings(out) 删掉（不再排序）。
//     -> TestRenderTextOrderIsStable 变红（乱序输入渲染出不同字节），
//        TestRenderTextOrdersBySeverity 变红（严重度顺序错乱）。
//  2. 把 writeFinding 里的 writeEvidence 调用删掉（证据不输出）。
//     -> TestRenderTextShowsHintAndEvidence 变红，
//        TestRenderTextKeepsEvidenceVerbatim 变红。
//  3. 把 writeFinding 里的 hint 分支删掉（行动指令不输出）。
//     -> TestRenderTextShowsHintAndEvidence 变红。
//  4. 把 headline 里 "没有发现空洞测试" 分支删掉（空报告结论丢失）。
//     -> TestRenderTextEmptyReportIsNotEmpty 变红。
//  5. 把 buildJSON 里的 Location 赋值删掉（JSON 丢位置字段）。
//     -> TestRenderJSONMatchesTextOrder 变红（location 变成空串）。
//
// 每次破坏都确认过变红后才还原，还原后全部测试重新变绿。
// ---------------------------------------------------------------------------

// sampleReport is a report that exercises every part of the renderer:
// all four severities, a finding with a location, one without, detail,
// evidence, a hint, and a file that has no line number.
func sampleReport() *core.Report {
	r := &core.Report{
		Project:      "sokebo/vacuous",
		RepoRoot:     "/tmp/vacuous-sample",
		Proven:       2,
		Vacuous:      1,
		Inconclusive: 1,
	}
	r.Add(core.Finding{
		Kind:     core.KindVacuousTest,
		Severity: core.SevHigh,
		Summary:  "这个测试在撤掉修复后依然是绿的 —— 它没有测到修复修的那个行为",
		Detail:   "断言只覆盖了输入构造，没有覆盖分隔符边界的合并行为。",
		File:     "internal/link/engine_test.go",
		Line:     136,
		Evidence: "TestAnalyzeDoesNotFuseAcrossSeparators 通过 (0.02s)",
		Hint:     "断言要落在可观察行为上，不要断言实现细节",
	})
	r.Add(core.Finding{
		Kind:     core.KindGuardNotInstalled,
		Severity: core.SevCritical,
		Summary:  ".githooks/pre-push 存在，但 core.hooksPath 没有设置 —— 守卫从未运行",
		Hint:     "跑 scripts/install-hooks.sh",
	})
	r.Add(core.Finding{
		Kind:     core.KindSkippedTest,
		Severity: core.SevInfo,
		Summary:  "这个测试被 t.Skip 掉了，本地不会执行",
		File:     "internal/scan/scan_test.go",
	})
	r.Add(core.Finding{
		Kind:     core.KindGuardNotPortable,
		Severity: core.SevWarn,
		Summary:  "守卫用绝对路径安装，目录一搬就失效",
		File:     ".githooks/pre-push",
		Line:     1,
	})
	return r
}

// scrambled returns the findings in a different order, same content.
func scrambled(fs []core.Finding) []core.Finding {
	out := make([]core.Finding, 0, len(fs))
	for i := len(fs) - 1; i >= 0; i-- {
		out = append(out, fs[i])
	}
	return out
}

func textOf(t *testing.T, r *core.Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderText(&buf, r); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	return buf.String()
}

func jsonOf(t *testing.T, r *core.Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	return buf.String()
}

// TestRenderTextCoversEverySeverity checks that no severity is silently
// dropped from the report.
func TestRenderTextCoversEverySeverity(t *testing.T) {
	r := &core.Report{Project: "demo"}
	r.Add(core.Finding{Kind: core.KindSkippedTest, Severity: core.SevInfo, Summary: "info note"})
	r.Add(core.Finding{Kind: core.KindGuardNotPortable, Severity: core.SevWarn, Summary: "warn note"})
	r.Add(core.Finding{Kind: core.KindVacuousTest, Severity: core.SevHigh, Summary: "high note"})
	r.Add(core.Finding{Kind: core.KindGuardNotInstalled, Severity: core.SevCritical, Summary: "critical note"})

	out := textOf(t, r)

	for _, want := range []string{"[CRITICAL]", "[    HIGH]", "[    WARN]", "[    INFO]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing severity marker %q:\n%s", want, out)
		}
	}
	// The headline names the two severities that block a release.
	for _, want := range []string{"严重", "高危"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing severity label %q:\n%s", want, out)
		}
	}
	// Each finding must carry its own kind too.
	for _, want := range []string{
		string(core.KindSkippedTest),
		string(core.KindGuardNotPortable),
		string(core.KindVacuousTest),
		string(core.KindGuardNotInstalled),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing kind %q:\n%s", want, out)
		}
	}
}

// TestRenderTextEmptyReportIsNotEmpty checks that a clean run still says
// something. An empty report looks like a crashed program in CI.
func TestRenderTextEmptyReportIsNotEmpty(t *testing.T) {
	out := textOf(t, &core.Report{Project: "demo"})

	if strings.TrimSpace(out) == "" {
		t.Fatal("empty report rendered nothing at all")
	}
	// The headline must say that nothing was judged, not merely that
	// nothing was found. Those are different, and a window of merge
	// commits produces the first while looking like the second.
	if !strings.Contains(out, "没有可判的提交") {
		t.Errorf("clean report does not state the conclusion:\n%s", out)
	}
	if !strings.Contains(out, "proven 0") {
		t.Errorf("clean report does not state the tallies:\n%s", out)
	}
}

// TestRenderTextEmptyReportStillCountsProven checks the clean-report
// headline says what was actually checked.
func TestRenderTextEmptyReportStillCountsProven(t *testing.T) {
	out := textOf(t, &core.Report{Project: "demo", Proven: 3})
	if !strings.Contains(out, "3 个修复都有测试守住") {
		t.Errorf("clean report does not say what was checked:\n%s", out)
	}
}

// TestRenderTextOrderIsStable is the core requirement: two runs on the same
// tree must produce byte-identical reports, so that a report can live in a
// git diff.
func TestRenderTextOrderIsStable(t *testing.T) {
	base := sampleReport()

	// Same report, rendered twice.
	first := textOf(t, base)
	second := textOf(t, base)
	if first != second {
		t.Errorf("two renders of the same report differ:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	// Same findings, opposite input order.
	reordered := &core.Report{
		Project:      base.Project,
		RepoRoot:     base.RepoRoot,
		Proven:       base.Proven,
		Vacuous:      base.Vacuous,
		Inconclusive: base.Inconclusive,
		Findings:     scrambled(base.Findings),
	}
	if got := textOf(t, reordered); got != first {
		t.Errorf("input order changed the output:\n--- sorted input ---\n%s\n--- scrambled input ---\n%s", first, got)
	}
}

// TestRenderTextOrdersBySeverity checks that findings come out worst first.
func TestRenderTextOrdersBySeverity(t *testing.T) {
	out := textOf(t, sampleReport())

	critical := strings.Index(out, "[CRITICAL]")
	high := strings.Index(out, "[    HIGH]")
	warn := strings.Index(out, "[    WARN]")
	info := strings.Index(out, "[    INFO]")
	if critical < 0 || high < 0 || warn < 0 || info < 0 {
		t.Fatalf("not every severity made it into the report:\n%s", out)
	}
	if !(critical < high && high < warn && warn < info) {
		t.Errorf("findings are not ordered worst first (critical %d, high %d, warn %d, info %d):\n%s",
			critical, high, warn, info, out)
	}
}

// TestRenderTextDoesNotMutateReport checks that rendering leaves the
// caller's report alone, so that the JSON renderer and the text renderer
// cannot fight over the same slice.
func TestRenderTextDoesNotMutateReport(t *testing.T) {
	r := sampleReport()
	before := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		before = append(before, string(f.Kind))
	}

	_ = textOf(t, r)
	_ = jsonOf(t, r)

	after := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		after = append(after, string(f.Kind))
	}
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("rendering reordered the caller's report: %v -> %v", before, after)
	}
}

// TestRenderTextShowsLocation checks that a finding is anchored to a
// position the reader can jump to.
func TestRenderTextShowsLocation(t *testing.T) {
	cases := []struct {
		name string
		file string
		line int
	}{
		{"file and line", "internal/link/engine_test.go", 136},
		{"file only", "internal/link/engine_test.go", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := core.Finding{
				Kind:     core.KindVacuousTest,
				Severity: core.SevHigh,
				Summary:  "s",
				File:     c.file,
				Line:     c.line,
			}
			r := &core.Report{}
			r.Add(f)
			out := textOf(t, r)
			if !strings.Contains(out, f.Location()) {
				t.Errorf("output is missing location %q:\n%s", f.Location(), out)
			}
		})
	}
}

// TestRenderTextShowsUnanchoredFinding checks that a finding with no file
// still says where it belongs.
func TestRenderTextShowsUnanchoredFinding(t *testing.T) {
	r := &core.Report{}
	r.Add(core.Finding{
		Kind:     core.KindGuardNotInstalled,
		Severity: core.SevCritical,
		Summary:  "守卫没装上",
	})
	out := textOf(t, r)
	if !strings.Contains(out, "(仓库根)") {
		t.Errorf("output does not mark the finding as unanchored:\n%s", out)
	}
}

// TestRenderTextShowsHintAndEvidence checks that the two pieces of a
// finding that make it actionable are actually rendered.
func TestRenderTextShowsHintAndEvidence(t *testing.T) {
	out := textOf(t, sampleReport())

	evidence := "TestAnalyzeDoesNotFuseAcrossSeparators 通过 (0.02s)"
	if !strings.Contains(out, "证据: "+evidence) {
		t.Errorf("output is missing the evidence:\n%s", out)
	}
	hint := "断言要落在可观察行为上，不要断言实现细节"
	if !strings.Contains(out, "怎么修: "+hint) {
		t.Errorf("output is missing the hint:\n%s", out)
	}
	detail := "断言只覆盖了输入构造"
	if !strings.Contains(out, "说明: "+detail) {
		t.Errorf("output is missing the detail:\n%s", out)
	}
}

// TestRenderTextShowsTallies checks the footer.
func TestRenderTextShowsTallies(t *testing.T) {
	out := textOf(t, sampleReport())
	if !strings.Contains(out, "统计: proven 2 · vacuous 1 · 无测试 0 · 无法判定 1") {
		t.Errorf("output is missing the tallies:\n%s", out)
	}
	if !strings.Contains(out, "按类别: ") {
		t.Errorf("output is missing the per-kind counts:\n%s", out)
	}
	for _, want := range []string{
		string(core.KindVacuousTest) + " 1",
		string(core.KindGuardNotInstalled) + " 1",
		string(core.KindSkippedTest) + " 1",
		string(core.KindGuardNotPortable) + " 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing kind count %q:\n%s", want, out)
		}
	}
}

// TestRenderTextHasNoANSI checks that the text report stays pipe-friendly.
func TestRenderTextHasNoANSI(t *testing.T) {
	out := textOf(t, sampleReport())
	if strings.Contains(out, "\x1b[") {
		t.Errorf("text report contains ANSI escapes:\n%q", out)
	}
}

// TestRenderTextKeepsLinesWithinBudget checks the 80-column budget for the
// lines the renderer generates itself. Raw evidence is exempt: it is
// quoted output and must stay verbatim.
func TestRenderTextKeepsLinesWithinBudget(t *testing.T) {
	long := strings.Repeat("很长的一个句子用来把这一行撑开", 12)
	r := &core.Report{Project: strings.Repeat("a-very-long-project-name-", 5)}
	for _, f := range []core.Finding{
		{Kind: core.KindVacuousTest, Severity: core.SevHigh, Summary: long, Detail: long, Hint: long,
			File: "internal/link/engine_test.go", Line: 136},
		{Kind: core.KindGuardNotInstalled, Severity: core.SevCritical, Summary: long},
		{Kind: core.KindSkippedTest, Severity: core.SevInfo, Summary: long},
		{Kind: core.KindGuardNotPortable, Severity: core.SevWarn, Summary: long},
		{Kind: core.KindDiscardsError, Severity: core.SevWarn, Summary: long},
		{Kind: core.KindNoTestForFix, Severity: core.SevHigh, Summary: long},
		{Kind: core.KindCoupled, Severity: core.SevInfo, Summary: long},
		{Kind: core.KindEnvironment, Severity: core.SevHigh, Summary: long},
	} {
		r.Add(f)
	}
	out := textOf(t, r)
	for _, line := range strings.Split(out, "\n") {
		if displayWidth(line) > maxWidth {
			t.Errorf("line is %d columns wide, budget is %d: %q", displayWidth(line), maxWidth, line)
		}
	}
}

// TestRenderTextKeepsEvidenceVerbatim checks that evidence is quoted
// exactly, even when it is longer than the width budget.
func TestRenderTextKeepsEvidenceVerbatim(t *testing.T) {
	evidence := strings.Repeat("x", 200)
	r := &core.Report{}
	r.Add(core.Finding{
		Kind:     core.KindVacuousTest,
		Severity: core.SevHigh,
		Summary:  "s",
		Evidence: evidence,
	})
	out := textOf(t, r)
	if !strings.Contains(out, evidence) {
		t.Error("evidence was re-wrapped instead of quoted verbatim")
	}
}

// TestRenderJSONRoundTrip checks that the JSON report can be read back and
// re-rendered to the same bytes.
func TestRenderJSONRoundTrip(t *testing.T) {
	first := jsonOf(t, sampleReport())

	var doc jsonReport
	if err := json.Unmarshal([]byte(first), &doc); err != nil {
		t.Fatalf("RenderJSON output cannot be unmarshalled: %v", err)
	}

	second := jsonOf(t, reportFromJSON(t, doc))
	if first != second {
		t.Errorf("JSON round trip changed the document:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// TestRenderJSONIsUnmarshalable checks that the document is plain JSON that
// any consumer can read, not something that only this package understands.
func TestRenderJSONIsUnmarshalable(t *testing.T) {
	out := jsonOf(t, sampleReport())

	var generic map[string]any
	if err := json.Unmarshal([]byte(out), &generic); err != nil {
		t.Fatalf("RenderJSON output is not valid JSON: %v", err)
	}
	for _, key := range []string{"project", "repo_root", "summary", "findings"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("missing top-level key %q in %s", key, out)
		}
	}

	summary, ok := generic["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary is not an object: %s", out)
	}
	for _, key := range []string{"proven", "vacuous", "inconclusive", "no_test", "by_kind", "max_severity"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("missing summary key %q in %s", key, out)
		}
	}
	if got := summary["max_severity"]; got != "critical" {
		t.Errorf("max_severity = %v, want critical", got)
	}

	findings, ok := generic["findings"].([]any)
	if !ok {
		t.Fatalf("findings is not an array: %s", out)
	}
	if len(findings) != 4 {
		t.Fatalf("got %d findings, want 4", len(findings))
	}
	for i, raw := range findings {
		f, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("finding %d is not an object: %s", i, out)
		}
		for _, key := range []string{
			"kind", "severity", "summary", "detail", "file", "line", "location", "evidence", "hint",
		} {
			if _, ok := f[key]; !ok {
				t.Errorf("finding %d is missing key %q in %s", i, key, out)
			}
		}
	}
}

// TestRenderJSONEmptyReport checks that an empty report is still a valid
// document with the same shape.
func TestRenderJSONEmptyReport(t *testing.T) {
	out := jsonOf(t, &core.Report{Project: "demo"})

	var doc jsonReport
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("RenderJSON output cannot be unmarshalled: %v", err)
	}
	if doc.Findings == nil {
		t.Error("findings is null, want an empty array")
	}
	if len(doc.Findings) != 0 {
		t.Errorf("got %d findings, want 0", len(doc.Findings))
	}
	if doc.Summary.MaxSeverity != "info" {
		t.Errorf("max_severity = %q, want info", doc.Summary.MaxSeverity)
	}
	if len(doc.Summary.ByKind) != 0 {
		t.Errorf("by_kind = %v, want empty", doc.Summary.ByKind)
	}
}

// TestRenderJSONMatchesTextOrder checks that both renderers agree on the
// order of the findings.
func TestRenderJSONMatchesTextOrder(t *testing.T) {
	r := sampleReport()

	var doc jsonReport
	if err := json.Unmarshal([]byte(jsonOf(t, r)), &doc); err != nil {
		t.Fatalf("RenderJSON output cannot be unmarshalled: %v", err)
	}
	if len(doc.Findings) != len(r.Findings) {
		t.Fatalf("got %d findings, want %d", len(doc.Findings), len(r.Findings))
	}
	for i, f := range doc.Findings {
		want := sortedFindings(r)[i]
		if f.Kind != string(want.Kind) || f.Severity != want.Severity.String() {
			t.Errorf("finding %d = %s/%s, want %s/%s", i, f.Kind, f.Severity, want.Kind, want.Severity)
		}
		if f.Location != want.Location() {
			t.Errorf("finding %d location = %q, want %q", i, f.Location, want.Location())
		}
	}
}

// TestRenderersRejectNil checks that a nil report is an error, not a panic.
func TestRenderersRejectNil(t *testing.T) {
	if err := RenderText(&bytes.Buffer{}, nil); err == nil {
		t.Error("RenderText(nil report) returned no error")
	}
	if err := RenderJSON(&bytes.Buffer{}, nil); err == nil {
		t.Error("RenderJSON(nil report) returned no error")
	}
	if err := RenderText(nil, &core.Report{}); err == nil {
		t.Error("RenderText(nil writer) returned no error")
	}
	if err := RenderJSON(nil, &core.Report{}); err == nil {
		t.Error("RenderJSON(nil writer) returned no error")
	}
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) {
	return 0, errors.New("write failed")
}

// TestRenderersPropagateWriteErrors checks that a broken pipe is reported.
func TestRenderersPropagateWriteErrors(t *testing.T) {
	if err := RenderText(failingWriter{}, sampleReport()); err == nil {
		t.Error("RenderText swallowed a write error")
	}
	if err := RenderJSON(failingWriter{}, sampleReport()); err == nil {
		t.Error("RenderJSON swallowed a write error")
	}
}

// TestSampleOutput renders a representative report and logs it, so the
// shape of the report is visible in the test output.
func TestSampleOutput(t *testing.T) {
	out := textOf(t, sampleReport())
	t.Logf("\n%s", out)

	if !strings.Contains(out, "vacuous — 发现 4 个问题") {
		t.Errorf("unexpected headline:\n%s", out)
	}
}

// TestSampleOutputJSON logs the machine-readable shape.
func TestSampleOutputJSON(t *testing.T) {
	t.Logf("\n%s", jsonOf(t, sampleReport()))
}

// reportFromJSON rebuilds a report from its JSON shape. It lives in the
// test because it exists only to prove the round trip.
func reportFromJSON(t *testing.T, doc jsonReport) *core.Report {
	t.Helper()
	r := &core.Report{
		Project:      doc.Project,
		RepoRoot:     doc.RepoRoot,
		Proven:       doc.Summary.Proven,
		Vacuous:      doc.Summary.Vacuous,
		Inconclusive: doc.Summary.Inconclusive,
		NoTest:       doc.Summary.NoTest,
	}
	for _, f := range doc.Findings {
		sev, err := core.ParseSeverity(f.Severity)
		if err != nil {
			t.Fatalf("severity %q: %v", f.Severity, err)
		}
		r.Add(core.Finding{
			Kind:     core.Kind(f.Kind),
			Severity: sev,
			Summary:  f.Summary,
			Detail:   f.Detail,
			File:     f.File,
			Line:     f.Line,
			Evidence: f.Evidence,
			Hint:     f.Hint,
		})
	}
	return r
}

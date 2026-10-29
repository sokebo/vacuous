// Package report renders a vacuous run into the two shapes the tool ships:
// a text report a human reads and acts on, and a JSON report a machine can
// diff.
//
// The package is deliberately dumb. It never opens a repository, never runs
// a test, and never decides whether a finding is justified. It only arranges
// what core.Report already contains, and it does so deterministically: the
// same report always renders to the same bytes, so a report can be committed
// and reviewed in a diff.
//
// Nothing here uses ANSI escapes. The text report is meant to survive a pipe
// into a file, a CI log, or an editor without losing its structure.
package report

import (
	"strings"

	"github.com/sokebo/vacuous/internal/core"
)

// Layout constants. Everything drawn here is ASCII (rules, brackets) or
// plain text, because a report that lands in a Windows CI log must not
// turn into a box of mojibake.
const (
	// maxWidth is the column budget for the lines this package generates.
	// Raw evidence is exempt: it is quoted output and must stay verbatim.
	maxWidth = 80
	// bodyIndent prefixes every continuation line of a finding.
	bodyIndent = "      "
	// kindWidth is the fixed width of the kind column, so that locations
	// line up down the page.
	kindWidth = 20
	// ruleWidth is the width of the ASCII rule separating the sections.
	ruleWidth = 68
)

// severityLabel is the human name of a severity, used in the headline.
func severityLabel(s core.Severity) string {
	switch s {
	case core.SevCritical:
		return "critical"
	case core.SevHigh:
		return "high"
	case core.SevWarn:
		return "warn"
	case core.SevInfo:
		return "info"
	}
	return "unknown"
}

// severityTag is the fixed-width marker printed on every finding. The
// severity is right-aligned inside the brackets so that the marker column
// stays straight, whatever the severity is.
func severityTag(s core.Severity) string {
	name := strings.ToUpper(s.String())
	if name == "UNKNOWN" {
		name = "?"
	}
	if len(name) < 8 {
		name = strings.Repeat(" ", 8-len(name)) + name
	}
	return "[" + name + "]"
}

// sortedFindings returns a sorted copy of the report's findings.
//
// The copy matters. Rendering must not reorder the caller's report, and two
// renderers must not fight over the same slice. core.SortFindings is
// deterministic and total, so the copy is stable across runs.
func sortedFindings(r *core.Report) []core.Finding {
	out := make([]core.Finding, len(r.Findings))
	copy(out, r.Findings)
	core.SortFindings(out)
	return out
}

// wrapText breaks s into lines.
//
// The first line may be firstWidth columns wide; every line after it is
// restWidth columns wide, which is how a wrapped line keeps room for the
// hanging indent it will be printed with.
//
// Explicit newlines are preserved. A line is broken on a word boundary when
// the text has spaces to break on, and hard-broken when it does not — CJK
// text has no spaces, so a hard break is the only honest option.
func wrapText(s string, firstWidth, restWidth int) []string {
	if s == "" {
		return nil
	}
	if firstWidth < 8 {
		firstWidth = 8
	}
	if restWidth < 8 {
		restWidth = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapLine(para, firstWidth, restWidth)...)
	}
	return out
}

// wrapLine wraps a single paragraph. The first line uses firstWidth, the
// rest use restWidth.
//
// A paragraph that already fits is returned untouched, so the common case
// loses nothing. A paragraph that does not fit is broken on word
// boundaries; a single word too long for any line is hard-broken, because
// CJK text has no spaces and a hard break is the only honest option.
func wrapLine(s string, firstWidth, restWidth int) []string {
	if displayWidth(s) <= firstWidth {
		return []string{s}
	}

	var lines []string
	limit := firstWidth
	cur := ""
	curWidth := 0

	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur, curWidth = "", 0
		}
		limit = restWidth
	}

	for _, word := range strings.Fields(s) {
		ww := displayWidth(word)
		for ww > limit {
			if cur != "" {
				flush()
			}
			cut := cutToWidth(word, limit)
			lines = append(lines, cut)
			word = word[len(cut):]
			ww = displayWidth(word)
			limit = restWidth
		}
		switch {
		case cur == "":
			cur, curWidth = word, ww
		case curWidth+1+ww <= limit:
			cur += " " + word
			curWidth += 1 + ww
		default:
			flush()
			cur, curWidth = word, ww
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// cutToWidth returns the longest prefix of s that fits in width columns.
func cutToWidth(s string, width int) string {
	w := 0
	for i, r := range s {
		rw := runeWidth(r)
		if w+rw > width {
			return s[:i]
		}
		w += rw
	}
	return s
}

// displayWidth is the rendered width of a string in terminal columns.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth is the rendered width of a rune in terminal columns. East Asian
// wide characters count as two columns; everything else counts as one.
func runeWidth(r rune) int {
	if r < 0x1100 {
		return 1
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals, Kangxi, punctuation
		r >= 0x3041 && r <= 0x33FF,   // Kana, CJK compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK extension A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE6F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // Fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // Fullwidth signs
		r >= 0x20000 && r <= 0x3FFFD: // CJK extension B and beyond
		return 2
	}
	return 1
}

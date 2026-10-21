package cli

import (
	"io"
	"strconv"
	"testing"
)

// The default audit window.
//
// The tool promises to audit "the most recent commits on this branch" -
// twenty of them, roughly one working week. That promise is written in
// the README, in the flag help, and in the comment next to the constant.
//
// The promise was broken by a single line:
//
//	fs.IntVar(&opts.Limit, "limit", 0, "...")
//
// flag.IntVar writes its default into the variable the moment the flag is
// registered. So the 0 overwrote the 20 that had just been set - and 0
// means "no limit", i.e. "audit the entire history, however long it is".
//
// It went unnoticed because the effect only appears once a repository is
// longer than the window. On a repository with 35 commits the tool
// audited 35 of them, and every one of them was reported - including
// commits from months earlier that nobody asked about.
//
// The same default is what the tool's own CI runs with, so the cost was
// paid on every push: the self-check audited the whole history instead of
// the last twenty commits.

// TestDefaultAuditWindowIsTwentyCommits is the guard.
func TestDefaultAuditWindowIsTwentyCommits(t *testing.T) {
	opts, err := parse([]string{"--mode", "check"}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.Limit != 20 {
		t.Errorf("default audit window = %d, want 20", opts.Limit)
	}
}

// TestExplicitLimitStillWins makes sure the default did not become
// hard-coded in a way that ignores the flag.
func TestExplicitLimitStillWins(t *testing.T) {
	for _, want := range []int{0, 1, 5, 100} {
		opts, err := parse([]string{"--mode", "check", "--limit", strconv.Itoa(want)}, io.Discard)
		if err != nil {
			t.Fatalf("parse --limit %d: %v", want, err)
		}
		if opts.Limit != want {
			t.Errorf("--limit %d gave %d", want, opts.Limit)
		}
	}
}

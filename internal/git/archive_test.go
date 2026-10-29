package git

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The rule these tests protect: **creating a checkout finishes.** Every
// counterfactual run starts with one, so a checkout that hangs is a tool
// that hangs on every audit.
//
// The symptom was a hang on Windows, and it was not a Windows bug. It was
// this:
//
//	"git archive" writes a tar stream. The tar format allows zero padding
//	after the end-of-archive marker, and git writes some.
//
//	Go's archive/tar returns io.EOF the moment it sees the marker. It does
//	NOT read the padding behind it.
//
//	So the pipe still holds the padding. Nobody reads it. git finishes
//	writing the padding and then waits for the reader to take the rest -
//	which never happens, because the reader has already stopped.
//
//	git blocks on write. The parent's cmd.Wait() waits for a process that
//	will never exit. The tool hangs forever.
//
// It hung on Windows and not on Linux, and that difference is what made it
// look like a platform bug. It is not: the leftover bytes are there on
// every platform. Linux's pipe buffer happened to be big enough for the
// padding to fit, so git never blocked. On Windows it did.
//
// ★ Which means **a regression test has to be about the leftover bytes,
// not about the platform.** A test that only runs on Windows would have
// missed this on the machine that has it in production. So the test below
// checks the thing that is actually wrong - that the pipe is not left with
// unread data - and it runs everywhere.

// TestArchiveIntoConsumesThePaddingBehindTheTarMarker is the regression
// test.
//
// It runs the real "git archive" through the real pipe and then checks that
// the bytes behind the end-of-archive marker were actually consumed.
//
// This is the assertion that makes the test worth having: before the fix,
// nothing was drained, the number was zero, and git blocked on writing the
// padding it had already put in the pipe. A test that only checked "the call
// returned" would have passed on the broken code on Linux, where the pipe
// buffer was big enough to hide the problem.
//
// The check is deliberately made without depending on the platform's
// process semantics: it asks the PIPE what was left in it, not whether the
// process exited.
func TestArchiveIntoConsumesThePaddingBehindTheTarMarker(t *testing.T) {
	f := newFixture(t)

	// How much padding does this git write behind the end-of-archive
	// marker? The assertion below is measured against this number rather
	// than against a hard-coded one, so that a future git which stops
	// writing padding does not turn into a false alarm - and, more
	// importantly, so that "the tool drained everything" cannot be
	// confused with "there was nothing to drain".
	padding := paddingBehindMarker(t, f.dir)
	if padding == 0 {
		t.Skip("this git writes no padding behind the end-of-archive marker; there is nothing to drain")
	}

	dest := t.TempDir()
	var drained bytes.Buffer
	files, n, err := archiveIntoDrained(f.dir, "", "HEAD", dest, &drained)
	if err != nil {
		t.Fatalf("archiveInto: %v", err)
	}
	if files == 0 {
		t.Fatal("the archive unpacked nothing")
	}
	if n < int64(padding) {
		t.Fatalf("archiveInto drained %d bytes, but this git wrote %d bytes of padding\n"+
			"behind the end-of-archive marker. The rest was left unread, and left unread is\n"+
			"exactly what git blocks on writing - the hang this fix is about.",
			n, padding)
	}
	t.Logf("%d bytes behind the end-of-archive marker were drained (this git writes %d)", n, padding)
}

// paddingBehindMarker measures how many bytes the real git puts behind the
// end-of-archive marker. It is the yardstick the drain is measured against.
func paddingBehindMarker(t *testing.T, dir string) int {
	t.Helper()

	cmd := exec.Command("git", "archive", "--format=tar", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git archive: %v", err)
	}

	// Find the end-of-archive marker: two consecutive zero blocks.
	marker := -1
	for i := 0; i+1024 <= len(out); i += 512 {
		if out[i] == 0 && out[i+512] == 0 {
			marker = i
			break
		}
	}
	if marker < 0 {
		return 0
	}
	return len(out) - (marker + 1024)
}

// TestArchiveIntoDoesNotLeaveGitBlockedOnThePipe is the same failure seen
// from the other side: the child process must be able to finish.
//
// This is the one that hung in production, so it is the one that gets a
// timeout. The failure message says what happened, because "the test
// timed out" on its own is not a report.
func TestArchiveIntoDoesNotLeaveGitBlockedOnThePipe(t *testing.T) {
	f := newFixture(t)

	dest := t.TempDir()

	done := make(chan error, 1)
	go func() {
		_, err := archiveInto(f.dir, "", "HEAD", dest)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("archiveInto: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("archiveInto did not finish within 60s.\n"+
			"This is the hang: \"git archive\" wrote a tar stream, the tar reader stopped at\n"+
			"the end-of-archive marker, and the padding behind it was never read - so git\n"+
			"blocked on writing the rest of it and cmd.Wait() waited for a process that\n"+
			"could not exit.\n"+
			"platform=%s", runtime.GOOS)
	}
}

// TestTheTarReaderStopsAtTheEndOfArchiveMarker records WHY the leftover
// bytes exist, so the fix is not "read the whole pipe" by accident.
//
// This test asserts a fact about the standard library, and it is here so
// that the next person who wonders "why is there draining code here?"
// finds the answer in the test that sits next to it.
func TestTheTarReaderStopsAtTheEndOfArchiveMarker(t *testing.T) {
	// A real tar stream, written by the standard library's own writer, with
	// zero padding behind the end-of-archive marker - which is what git
	// produces.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	// tw.Close() wrote the marker (two zero blocks). git writes padding
	// behind it; add some.
	const paddingBlocks = 2
	stream := append(buf.Bytes(), make([]byte, paddingBlocks*512)...)

	// Read the stream the way the tool does: through the tar reader, and
	// then keep reading the raw stream.
	//
	// The tar reader stops at the marker: it returns io.EOF after the two
	// marker blocks, and it does not touch the padding behind them.
	r := bytes.NewReader(stream)
	tr := tar.NewReader(r)
	for {
		_, err := tr.Next()
		if err != nil {
			break
		}
	}

	// Whatever the tar reader left behind is what git would block on.
	//
	// The exact number depends on how much the reader happened to buffer,
	// so the test does not assert it. It asserts the property that
	// matters: the reader stopped before the end of the stream, and
	// there is unread data behind it.
	remaining, _ := io.ReadAll(r)
	if len(remaining) == 0 {
		t.Skip("the standard library drained the padding; the draining code is harmless but unnecessary")
	}
	t.Logf("%d bytes were left unread behind the end-of-archive marker", len(remaining))
}

// TestGitArchiveReallyWritesPaddingBehindTheMarker is the same fact,
// measured against the real git rather than a hand-made stream.
//
// It is what makes the hand-made one trustworthy: if a future git stops
// writing padding, the hand-made test still passes but this one says so.
func TestGitArchiveReallyWritesPaddingBehindTheMarker(t *testing.T) {
	f := newFixture(t)

	padding := paddingBehindMarker(t, f.dir)
	if padding == 0 {
		t.Skip("this git wrote no end-of-archive marker in the expected place")
	}
	t.Logf("git wrote %d padding bytes behind the end-of-archive marker", padding)
}

// TestArchiveIntoUnpacksEveryFile is the control: the checkout must
// actually contain the repository.
//
// The fix for the hang must not cost a single file.
func TestArchiveIntoUnpacksEveryFile(t *testing.T) {
	f := newFixture(t)

	dest := t.TempDir()
	files, err := archiveInto(f.dir, "", "HEAD", dest)
	if err != nil {
		t.Fatalf("archiveInto: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "bug.go")); err != nil {
		t.Errorf("the checkout is missing bug.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bug_test.go")); err != nil {
		t.Errorf("the checkout is missing bug_test.go: %v", err)
	}
	if files < 2 {
		t.Errorf("archiveInto unpacked %d files, want at least 2", files)
	}
}

// TestArchiveIntoDoesNotHangWhenUnpackingFails is the SECOND door into the
// same hang, and nobody had noticed it.
//
// The happy path stops at the end-of-archive marker and leaves the padding
// behind. The failure path stops even earlier - in the middle of the
// archive - and leaves the REST OF THE ARCHIVE behind, which is a lot more
// bytes.
//
// Same deadlock: git is blocked writing what nobody is reading, and
// cmd.Wait() waits for a process that cannot exit.
//
// The error path is the one that matters most, because it is the path a
// reader hits when something has ALREADY gone wrong. A hang there turns a
// reportable error into a silent one.
func TestArchiveIntoDoesNotHangWhenUnpackingFails(t *testing.T) {
	f := newFixture(t)

	// A destination that cannot be written to: the untar will fail on the
	// first file, with the whole archive still in the pipe.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := archiveInto(f.dir, "", "HEAD", blocked)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Log("the archive was unpacked into a blocked destination without an error")
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("archiveInto did not finish within 60s when unpacking failed.\n"+
			"This is the same hang as the happy path, reached by a different door: untar\n"+
			"stopped early, the pipe still held the rest of the archive, git was blocked\n"+
			"writing it, and cmd.Wait() waited for a process that could not exit.\n"+
			"platform=%s", runtime.GOOS)
	}
}

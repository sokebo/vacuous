package detect

import (
	"strings"
	"testing"

	"github.com/sokebo/vacuous/internal/core"
)

// The same act - stop the code right there - has several spellings, and the
// emptiness checker used to answer differently for each of them:
//
//	panic("...")      → neither: the finding disappeared
//	log.Fatal("...")  → "the assertion passes"
//	os.Exit(1)        → "the guard was skipped"
//
// Two of those answers were wrong, and one of them lost the finding
// altogether - with no symptom at all: the report simply had nothing to say,
// which looks exactly like a clean repository.
//
// Each case below is the same shape: a value that comes back empty when the
// load fails, compared against zero, with a branch that stops the code.
//
// The report has to say which of the two things happened, because they are
// not the same thing: a branch that fails the test makes the assertion a
// decoration, and a branch that stops the code makes the guard never fire.
//
// Negative control: take the stopping out of the branch and the finding has
// to disappear - see TestABranchThatOnlyLogsIsNotAStop.
func TestEveryWayOfStoppingTheCodeIsSeen(t *testing.T) {
	cases := []struct {
		name string
		file string
		src  string
		// want is a fragment of the summary: which of the two things the
		// branch does.
		want string
		// notWant is a fragment the summary must not contain.
		notWant string
	}{
		{
			name:    "a panic in ordinary code stops the program",
			file:    "seed.go",
			want:    "检查被跳过",
			notWant: "断言",
			src: `package demo

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func Seed(st *store) {
	existing, _ := st.Count()
	if existing > 0 {
		panic("库里已有数据，要继续请先清空")
	}
}
`,
		},
		{
			name:    "a log.Fatal in ordinary code stops the program",
			file:    "seed.go",
			want:    "检查被跳过",
			notWant: "断言",
			src: `package demo

import "log"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func Seed(st *store) {
	existing, _ := st.Count()
	if existing > 0 {
		log.Fatal("库里已有数据，要继续请先清空")
	}
}
`,
		},
		{
			name:    "an os.Exit in ordinary code stops the program",
			file:    "seed.go",
			want:    "检查被跳过",
			notWant: "断言",
			src: `package demo

import "os"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func Seed(st *store) {
	existing, _ := st.Count()
	if existing > 0 {
		os.Exit(1)
	}
}
`,
		},
		{
			name: "a panic in a test file fails the test",
			file: "seed_test.go",
			want: "断言却要求",
			src: `package demo

import "testing"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func TestSeed(t *testing.T) {
	st := &store{}
	existing, _ := st.Count()
	if existing > 0 {
		panic("库里已有数据，要继续请先清空")
	}
}
`,
		},
		{
			name: "a t.Fatal in a test file fails the test",
			file: "seed_test.go",
			want: "断言却要求",
			src: `package demo

import "testing"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func TestSeed(t *testing.T) {
	st := &store{}
	existing, _ := st.Count()
	if existing > 0 {
		t.Fatalf("库里已有数据，要继续请先清空")
	}
}
`,
		},
		{
			name: "a require.Equal in a test file fails the test",
			file: "seed_test.go",
			want: "断言却要求",
			src: `package demo

import (
	"testing"

	"demo/require"
)

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func TestSeed(t *testing.T) {
	st := &store{}
	existing, _ := st.Count()
	if existing > 0 {
		require.Equal(t, 0, existing)
	}
}
`,
		},
		{
			name: "an assert.Equal in a test file fails the test",
			file: "seed_test.go",
			want: "断言却要求",
			src: `package demo

import (
	"testing"

	"demo/assert"
)

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func TestSeed(t *testing.T) {
	st := &store{}
	existing, _ := st.Count()
	if existing > 0 {
		assert.Equal(t, 0, existing)
	}
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
			// The assertion libraries are local stand-ins for testify's, so
			// the fixture compiles and needs nothing from the network. What
			// the checker reads is the package NAME at the call site.
			write(t, dir, "require/require.go", requireStub)
			write(t, dir, "assert/assert.go", assertStub)
			write(t, dir, tc.file, tc.src)
			commit(t, dir)

			findings, err := DiscardedErrors(dir, []string{tc.file})
			if err != nil {
				t.Fatalf("DiscardedErrors: %v", err)
			}
			if len(findings) == 0 {
				t.Fatalf("a branch that stops the code must be reported, got no finding")
			}
			f := findings[0]
			if f.Severity < core.SevHigh {
				t.Errorf("severity = %v, want high", f.Severity)
			}
			if !strings.Contains(f.Summary, tc.want) {
				t.Errorf("summary = %q, want it to contain %q", f.Summary, tc.want)
			}
			if tc.notWant != "" && strings.Contains(f.Summary, tc.notWant) {
				t.Errorf("summary = %q, must not contain %q", f.Summary, tc.notWant)
			}
			if tc.notWant != "" && strings.Contains(f.Detail, tc.notWant) {
				t.Errorf("detail = %q, must not contain %q", f.Detail, tc.notWant)
			}
		})
	}
}

// TestABranchThatOnlyLogsIsNotAStop is the negative control for the table
// above.
//
// A branch that writes a line down is not a protective check: nothing stops,
// so there is no guard to skip and no assertion to decorate. Reporting it
// would be noise, and noise is what makes people switch a checker off.
func TestABranchThatOnlyLogsIsNotAStop(t *testing.T) {
	cases := []struct {
		name string
		file string
		src  string
	}{
		{
			name: "ordinary code",
			file: "seed.go",
			src: `package demo

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func Seed(st *store) {
	existing, _ := st.Count()
	if existing > 0 {
		println("库里已有数据")
	}
}
`,
		},
		{
			name: "a test file",
			file: "seed_test.go",
			src: `package demo

import "testing"

type store struct{ items int }

func (s *store) Count() (int, error) { return s.items, nil }

func TestSeed(t *testing.T) {
	st := &store{}
	existing, _ := st.Count()
	if existing > 0 {
		t.Logf("库里已有数据")
	}
}
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			write(t, dir, "go.mod", "module demo\n\ngo 1.21\n")
			write(t, dir, tc.file, tc.src)
			commit(t, dir)

			findings, err := DiscardedErrors(dir, []string{tc.file})
			if err != nil {
				t.Fatalf("DiscardedErrors: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("a branch that only logs must not be reported, got: %+v", findings)
			}
		})
	}
}

// requireStub stands in for testify's require package.
//
// Equal fails the test when the two numbers differ - which is what makes a
// require.Equal in a guarded branch an assertion, and what the emptiness
// checker used to miss.
const requireStub = `package require

import "testing"

func Equal(t *testing.T, expected, actual int) {
	t.Helper()
	if expected != actual {
		t.Fatalf("expected %d, got %d", expected, actual)
	}
}
`

// assertStub stands in for testify's assert package.
const assertStub = `package assert

import "testing"

func Equal(t *testing.T, expected, actual int) {
	t.Helper()
	if expected != actual {
		t.Errorf("expected %d, got %d", expected, actual)
	}
}
`

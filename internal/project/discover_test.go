package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/project"
)

// mkProject creates .incus-dev/dev.yml under root.
func mkProject(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".incus-dev")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.yml"), []byte("schema: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverInProjectRoot(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)

	got, err := project.Discover(root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if want := mustEvalSymlinks(t, root); got.Root != want {
		t.Errorf("Root = %q, want %q", got.Root, want)
	}
	if want := filepath.Join(got.Root, ".incus-dev", "dev.yml"); got.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want %q", got.ConfigPath, want)
	}
}

func TestDiscoverFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)
	nested := filepath.Join(root, "src", "foo", "bar")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := project.Discover(nested)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if want := mustEvalSymlinks(t, root); got.Root != want {
		t.Errorf("Root = %q, want %q", got.Root, want)
	}
}

func TestDiscoverReturnsNearestProject(t *testing.T) {
	outer := t.TempDir()
	mkProject(t, outer)
	inner := filepath.Join(outer, "sub", "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	mkProject(t, inner)

	got, err := project.Discover(inner)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if want := mustEvalSymlinks(t, inner); got.Root != want {
		t.Errorf("Root = %q, want %q (the nearest project must win)", got.Root, want)
	}
}

func TestDiscoverRelativeStartDir(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)
	t.Chdir(root)

	got, err := project.Discover(".")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if !filepath.IsAbs(got.Root) {
		t.Errorf("Root = %q, want an absolute path", got.Root)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := project.Discover(dir)
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("Discover() error = %v, want ErrNotFound", err)
	}
}

// With .incus-dev/ present but no dev.yml, the error message says so.
func TestDiscoverConfigDirWithoutConfigFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".incus-dev"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := project.Discover(root)
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("Discover() error = %v, want ErrNotFound", err)
	}
	// "exists but ... is missing", not the generic "was not found in ... or any
	// parent directory": both name .incus-dev/dev.yml, so matching that alone
	// would pass either way.
	if !strings.Contains(err.Error(), "exists but") {
		t.Errorf("error = %q, want it to say the directory is there without the file", err.Error())
	}
	if want := filepath.Join(root, ".incus-dev"); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name %q", err.Error(), want)
	}
}

func TestDiscoverGitRootIsNotRequired(t *testing.T) {
	// It works outside a Git repository (the dependency on Git is incidental).
	root := t.TempDir()
	mkProject(t, root)

	if _, err := project.Discover(root); err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
}

// A start directory that is not there is a mistake, not a reason to use an
// ancestor's project.
func TestDiscoverRejectsMissingStartDir(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)

	missing := filepath.Join(root, "services", "api-v2")

	proj, err := project.Discover(missing)

	if err == nil {
		t.Fatalf("Discover() = %+v, want an error rather than the project above", proj)
	}
	if errors.Is(err, project.ErrNotFound) {
		t.Errorf("error = %v, want a missing directory not to be reported as ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "api-v2") {
		t.Errorf("error = %v, want it to name the directory asked for", err)
	}
}

// -C pointed at a file is a mistake too.
func TestDiscoverRejectsAFileAsStartDir(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)

	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := project.Discover(file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %v, want it to say the start is not a directory", err)
	}
}

// A file where .incus-dev/ would be is not a project, so the search carries on
// upwards rather than stopping.
func TestDiscoverPassesAFileNamedLikeTheConfigDir(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)

	mid := filepath.Join(root, "mid")
	if err := os.MkdirAll(mid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mid, ".incus-dev"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, err := project.Discover(mid)
	if err != nil {
		t.Fatalf("Discover() error = %v, want the project above to be found", err)
	}
	if proj.Root != mustEvalSymlinks(t, root) {
		t.Errorf("Root = %q, want %q", proj.Root, root)
	}
}

func mustEvalSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// An unreadable directory is reported as an error, not as "not found".
func TestDiscoverReportsUnreadableDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot reproduce a permission failure as root")
	}

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(locked, ".incus-dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := project.Discover(locked)
	if err == nil {
		t.Fatal("Discover() = nil error, want error")
	}
	if errors.Is(err, project.ErrNotFound) {
		t.Errorf("error = %v, want a permission problem not to be reported as ErrNotFound", err)
	}
}

// A dev.yml that exists but is not a file is not a missing dev.yml. Telling
// the user to create what is already there sends them in a circle.
func TestDiscoverReportsAConfigOfTheWrongKind(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, path string)
	}{
		{"a directory", func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		// The one that matters most: opening a fifo blocks until a writer
		// arrives, and nothing downstream can interrupt that.
		{"a named pipe", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("cannot create a fifo: %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".incus-dev"), 0o755); err != nil {
				t.Fatal(err)
			}
			tt.make(t, filepath.Join(root, ".incus-dev", "dev.yml"))

			_, err := project.Discover(root)
			if err == nil {
				t.Fatal("Discover() = nil error, want the wrong kind reported")
			}
			if strings.Contains(err.Error(), "is missing") {
				t.Errorf("error = %q, want it not to call this missing", err.Error())
			}
			if !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("error = %q, want it to say what is wrong", err.Error())
			}
		})
	}
}

// A dev.yml of the wrong kind in a subdirectory does not hide the real
// project above it. The search is upward for a reason.
func TestDiscoverKeepsLookingPastAConfigOfTheWrongKind(t *testing.T) {
	root := t.TempDir()
	mkProject(t, root)

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".incus-dev", "dev.yml"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := project.Discover(sub)
	if err != nil {
		t.Fatalf("Discover() error = %v, want the project above found", err)
	}
	if got.Root != root {
		t.Errorf("Root = %q, want %q", got.Root, root)
	}
}

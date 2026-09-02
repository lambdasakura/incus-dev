package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if want := filepath.Join(".incus-dev", "dev.yml"); !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
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

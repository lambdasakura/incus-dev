package project_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/project"
)

// mkProject は root 配下に .incus-dev/dev.yml を作る。
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
		t.Errorf("Root = %q, want %q (最も近いプロジェクトを返すこと)", got.Root, want)
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
		t.Errorf("Root = %q, 絶対パスであること", got.Root)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := project.Discover(dir)
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("Discover() error = %v, want ErrNotFound", err)
	}
}

// .incus-dev/ はあるが dev.yml が無い場合、エラーメッセージでそれを示す。
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
		t.Errorf("error = %q, %q を含むこと", err.Error(), want)
	}
}

func TestDiscoverGitRootIsNotRequired(t *testing.T) {
	// Gitリポジトリでなくても動作すること（REQ: Gitへの依存は副次的）
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

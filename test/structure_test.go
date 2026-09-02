package test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// idev carries no environment-specific assets (REQ-007, spec 08-testing.md
// 8.2).
//
// It walks the whole repository. examples/ and test/ are out of scope, being
// samples for users and test fixtures (spec 02-repository-layout.md 2.3).
func TestNoEnvironmentSpecificAssets(t *testing.T) {
	forbidden := map[string]string{
		"ansible":          "shared playbooks or roles",
		"profiles":         "shared Incus profiles",
		"roles":            "shared Ansible roles",
		"requirements.yml": "a shared collection definition",
	}
	skipDirs := map[string]bool{
		".git": true, "bin": true, "examples": true, "test": true, "docs": true,
	}

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel("..", path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() && skipDirs[rel] {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}

		if reason, bad := forbidden[d.Name()]; bad {
			t.Errorf("%s exists (%s). Under REQ-007, environment-specific content "+
				"belongs in a project's .incus-dev/, not in idev", rel, reason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// No OS-specific command sneaks into the implementation (REQ-007).
//
// Checking file names alone would not catch "written straight into a .go", so
// the bodies are checked for package-manager invocations.
func TestNoOSSpecificCommandsInImplementation(t *testing.T) {
	// The one exception spec 06-provisioning.md 6.3.2 allows.
	allowed := map[string]string{
		"internal/provision/provision.go": "the default bootstrap, which can be overridden or disabled",
	}
	managers := []string{"apt-get", "apt install", "dnf ", "yum ", "apk add", "pacman -S", "zypper "}

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// examples/, docs/ and test/ are examples for users, and out of scope.
			switch filepath.Base(path) {
			case ".git", "bin", "examples", "docs", "test":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel := filepath.ToSlash(strings.TrimPrefix(path, "../"))
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range managers {
			if !strings.Contains(string(data), m) {
				continue
			}
			if reason, ok := allowed[rel]; ok {
				t.Logf("%s: %q (%s)", rel, m, reason)
				continue
			}
			t.Errorf("%s contains %q. Under REQ-007, OS-specific steps belong in "+
				"a project's .incus-dev/, not in idev", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// The JSON Schema is the only thing embedded in the binary
// (spec 02-repository-layout.md 2.4).
func TestOnlySchemasAreEmbedded(t *testing.T) {
	var files []string

	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		// Test files are not in the binary, and are out of scope.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "//go:embed") {
			files = append(files, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	want := []string{"../schemas/embed.go"}
	if len(files) != len(want) || files[0] != want[0] {
		t.Errorf("files using go:embed = %v, want %v\n"+
			"the JSON Schema is the only thing idev may embed (REQ-007)", files, want)
	}
}

// The packages depend on each other only in the directions CLAUDE.md and spec
// 07-implementation.md 7.1 declare.
//
// Nothing checked this, although 02-repository-layout.md says this file does:
// internal/provision had grown an edge to internal/project for one constant.
func TestPackageDependencyDirections(t *testing.T) {
	const mod = "github.com/lambdasakura/incus-dev/"

	allowed := map[string][]string{
		"cmd/idev":                   {"internal/cli"},
		"internal/cli":               {"internal/project", "internal/config", "internal/incus", "internal/provision", "internal/runner"},
		"internal/provision":         {"internal/config", "internal/incus", "internal/runner"},
		"internal/incus":             {},
		"internal/config":            {"schemas"},
		"internal/project":           {},
		"internal/runner":            {},
		"schemas":                    {},
		"internal/incus/incustest":   {"internal/incus"},
		"internal/runner/runnertest": {"internal/runner"},
	}

	for pkg, want := range allowed {
		t.Run(pkg, func(t *testing.T) {
			// The implementation's own direct imports. A transitive one is
			// the business of the package that imports it, and a test may
			// reach for a fake the implementation must not.
			out, err := exec.Command("go", "list", "-f",
				"{{range .Imports}}{{.}} {{end}}", "../"+pkg).Output()
			if err != nil {
				t.Fatalf("go list: %v", err)
			}
			for _, dep := range strings.Fields(string(out)) {
				rel, ok := strings.CutPrefix(dep, mod)
				if !ok || rel == pkg {
					continue
				}
				if !slices.Contains(want, rel) {
					t.Errorf("%s depends on %s, which the layout does not allow", pkg, rel)
				}
			}
		})
	}
}

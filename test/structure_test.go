package test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/project"
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

// The two copies of the configuration directory's name agree.
//
// internal/config carries it so the packages reading dev.yml need not depend
// on internal/project; nothing but a comment kept the values equal, and a
// change to one would quietly stop ansible.cfg being found.
func TestConfigDirNamesAgree(t *testing.T) {
	if config.ConfigDir != project.ConfigDir {
		t.Errorf("config.ConfigDir = %q, project.ConfigDir = %q, want them equal",
			config.ConfigDir, project.ConfigDir)
	}
}

// CLAUDE.md and spec 07-implementation.md 7.2 confine external command
// execution to internal/runner, and os.Exit to cmd/idev/main.go. Nothing
// enforced either: the gosec rule that would notice an exec is excluded
// repo-wide, and no test looked. An architecture held up by a document alone
// stops being true quietly.
func TestArchitecturalConstraintsHold(t *testing.T) {
	// Imports come from the parser rather than a pattern: a single-line
	// `import "os/exec"` does not look like one inside a block.
	t.Run("os/exec outside internal/runner", func(t *testing.T) {
		forEachSourceFile(t, func(path, dir string, body []byte) {
			if dir == "internal/runner" || strings.HasPrefix(dir, "internal/runner/") {
				return
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, body, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			for _, imp := range file.Imports {
				if imp.Path.Value == `"os/exec"` {
					t.Errorf("%s: external commands are run only through internal/runner", path)
				}
			}
		})
	})

	t.Run("os.Exit outside main", func(t *testing.T) {
		pattern := regexp.MustCompile(`\bos\.Exit\(`)
		forEachSourceFile(t, func(path, dir string, body []byte) {
			if dir == "cmd/idev" {
				return
			}
			if pattern.Match(body) {
				t.Errorf("%s: every package but main returns an error", path)
			}
		})
	})
}

// forEachSourceFile visits the repository's non-test Go files. Tests may reach
// for either construct to set a scenario up.
func forEachSourceFile(t *testing.T, visit func(path, dir string, body []byte)) {
	t.Helper()

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == "dist"):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visit(path, filepath.ToSlash(filepath.Dir(strings.TrimPrefix(path, "../"))), body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The gates in the Makefile are load-bearing and nothing else reads them.
//
// `check` once fell back to gofmt and go vet when golangci-lint was absent
// and still exited 0, which is how an unformatted file reached two commits.
// Reverting that is a one-word edit no test would notice.
//
// The assertions are about what the targets depend on and what they refuse
// to run without, not about how the file is laid out: a test that fails on
// whitespace gets deleted rather than fixed.
func TestMakefileKeepsItsGates(t *testing.T) {
	makefile := readFile(t, "../Makefile")

	if got := prerequisites(makefile, "check"); !slices.Contains(got, "strict-lint") {
		t.Errorf("check depends on %v, want strict-lint among them: it has to "+
			"require the real linter rather than fall back to gofmt and go vet", got)
	}

	// And the two targets that cannot do their job without a tool say so,
	// rather than passing quietly.
	for _, want := range []struct{ tool, target string }{
		{"golangci-lint", "check"},
		{"jq", "vuln"},
	} {
		if !strings.Contains(makefile, want.tool+" is required by") {
			t.Errorf("the Makefile no longer refuses to run %s without %s",
				want.target, want.tool)
		}
	}

	// And strict-lint has to stop the build, not merely say something: the
	// message alone would leave `exit 0` looking correct.
	if !strings.Contains(makefile, "run 'make tools'\"; exit 1; }") {
		t.Error("strict-lint no longer exits non-zero when golangci-lint is missing")
	}

	// CI runs the same targets, so a check cannot pass locally and be absent
	// from the build, or the reverse. Commented out counts as absent, which
	// a substring search would not notice.
	ci := readFile(t, "../.github/workflows/ci.yml")
	for _, target := range []string{"make vuln", "make tidy", "make cover"} {
		if !runsInCI(ci, target) {
			t.Errorf("ci.yml no longer runs %q", target)
		}
	}
}

// runsInCI reports whether a workflow actually runs the command, as opposed
// to mentioning it in a comment.
func runsInCI(workflow, command string) bool {
	for _, line := range strings.Split(workflow, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		if rest, ok := strings.CutPrefix(line, "run:"); ok &&
			strings.Contains(rest, command) && !strings.HasPrefix(strings.TrimSpace(rest), "#") {
			return true
		}
	}
	return false
}

// prerequisites returns what a make target depends on.
func prerequisites(makefile, target string) []string {
	for _, line := range strings.Split(makefile, "\n") {
		name, deps, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != target || strings.HasPrefix(line, "\t") {
			continue
		}
		return strings.Fields(deps)
	}
	return nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

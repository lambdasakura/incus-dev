package test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

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
		"internal/incus/contract":    {"internal/incus"},
		"internal/config":            {"schemas"},
		"internal/project":           {},
		"internal/runner":            {},
		"schemas":                    {},
		"internal/incus/incustest":   {"internal/incus"},
		"internal/runner/runnertest": {"internal/runner"},
	}

	// go list, not the map's own keys: a package added under internal/ was
	// silently unchecked rather than reported, which is how
	// internal/incus/contract came to be allowed to import anything.
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}}", "../...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(pkg, mod)
		if !ok || rel == "test" || strings.HasPrefix(rel, "test/") {
			continue
		}
		if _, listed := allowed[rel]; !listed {
			t.Errorf("package %s is not in the layout, so nothing checks what it imports", rel)
		}
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

	// The panicking constructor used to be the one called NewApp, so the
	// obvious name was the one no user should reach. Renaming it is only half
	// the fix: nothing stops the next caller from picking it up again, and a
	// panic reaches the user as a stack trace instead of a message.
	t.Run("MustNewApp outside tests", func(t *testing.T) {
		pattern := regexp.MustCompile(`\bMustNewApp\(`)
		forEachSourceFile(t, func(path, dir string, body []byte) {
			if dir == "internal/cli" && filepath.Base(path) == "app.go" {
				return // Its definition.
			}
			if pattern.Match(body) {
				t.Errorf("%s: MustNewApp panics when the instance name cannot be "+
					"derived. Anything a user runs calls NewApp and reports the error",
					path)
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

// The gates in the Makefile and the workflows are load-bearing and nothing
// else reads them.
//
// `check` once fell back to gofmt and go vet when golangci-lint was absent
// and still exited 0, which is how an unformatted file reached two commits.
// A tag does not trigger ci.yml, so release.yml carries its own gate; losing
// it ships whatever is on the branch.
//
// Both are read by asking the tool rather than by parsing the file. Five
// rounds of hand-written parsing of Makefile and YAML text produced a defect
// every time, in both directions: a gate switched off that read as on, and a
// reformat that failed the build.
func TestBuildGatesHold(t *testing.T) {
	t.Run("check requires the real linter and runs the tests", func(t *testing.T) {
		// make -p prints its own database, so this is what make will do, not
		// what the file looks like.
		got := makePrerequisites(t, "..", "check")
		for _, want := range []struct{ dep, why string }{
			{"strict-lint", "it has to require the real linter, not fall back to gofmt and go vet"},
			{"test", "CLAUDE.md makes check the gate run before every commit"},
			{"tidy", "an untidy go.mod fails CI and nothing else here would catch it"},
		} {
			if !slices.Contains(got, want.dep) {
				t.Errorf("check depends on %v, want %s among them: %s", got, want.dep, want.why)
			}
		}
	})

	// Each target that needs a tool must stop at its own guard, rather than
	// falling through to the tool and failing by accident.
	for _, target := range []string{"strict-lint", "vuln"} {
		t.Run(target+" without its tool", func(t *testing.T) {
			cmd := exec.Command("make", target)
			cmd.Dir = ".."
			cmd.Env = append(os.Environ(), "PATH=/nonexistent")

			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Errorf("make %s succeeded without its tool:\n%s", target, out)
			}
			if !strings.Contains(string(out), "is required by") {
				t.Errorf("make %s said %q, want it to name what is missing", target, out)
			}
			for _, ranOn := range []string{"not found", "No such file"} {
				if strings.Contains(string(out), ranOn) {
					t.Errorf("make %s ran on past its guard:\n%s", target, out)
				}
			}
		})
	}

	// The three shapes GitHub allows, so a rewrite that changes nothing about
	// when CI runs cannot take the trigger check out with an unmarshal error.
	t.Run("the trigger shapes all read", func(t *testing.T) {
		for _, body := range []string{
			"on:\n  push:\n    branches: [main]\n  pull_request:\njobs: {}\n",
			"on: [push, pull_request]\njobs: {}\n",
			"on: push\njobs: {}\n",
		} {
			var wf workflow
			if err := yaml.Unmarshal([]byte(body), &wf); err != nil {
				t.Fatalf("%q: %v", body, err)
			}
			if _, ok := wf.OnAsBoolean["push"]; !ok {
				t.Errorf("%q gave triggers %v, want push among them", body, wf.OnAsBoolean)
			}
		}
	})

	t.Run("CI is triggered at all", func(t *testing.T) {
		// Every other check here asks what CI runs. None of them asks whether
		// CI runs: reducing on: to workflow_dispatch leaves them all green
		// and no gate is reached on a push or a pull request.
		on := workflowTriggers(t, "../.github/workflows/ci.yml")
		for _, event := range []string{"push", "pull_request"} {
			if _, ok := on[event]; !ok {
				t.Errorf("ci.yml is not triggered by %s; it is triggered by %v",
					event, slices.Sorted(maps.Keys(on)))
			}
		}
	})

	t.Run("CI runs the same targets", func(t *testing.T) {
		targets := makeTargets(t, "..")
		ci := workflowCommands(t, "../.github/workflows/ci.yml")
		for _, target := range []string{"vuln", "tidy", "cover"} {
			if !slices.ContainsFunc(ci, func(c string) bool { return invokesTarget(c, targets, target) }) {
				t.Errorf("ci.yml no longer runs make %s; it runs %v", target, ci)
			}
		}
	})

	t.Run("a tag cannot ship untested code", func(t *testing.T) {
		const path = "../.github/workflows/release.yml"

		targets := makeTargets(t, "..")
		cmds := workflowCommands(t, path)
		for _, target := range []string{"tidy", "test"} {
			if !slices.ContainsFunc(cmds, func(c string) bool { return invokesTarget(c, targets, target) }) {
				t.Errorf("release.yml no longer runs make %s; it runs %v", target, cmds)
			}
		}

		var wf workflow
		if err := yaml.Unmarshal([]byte(readFile(t, path)), &wf); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(wf.Jobs["release"].Needs, "test") {
			t.Errorf("the release job waits for %v, want the test job among them",
				wf.Jobs["release"].Needs)
		}
	})
}

// workflow is the part of a GitHub workflow these checks read.
type workflow struct {
	Jobs map[string]struct {
		// A single dependency may be written as a string or as a list, so
		// this takes whatever YAML gives and normalises it.
		Needs stringOrList `json:"needs"`
		// If and ContinueOnError decide whether a gate is really a gate;
		// see enforced.
		If              json.RawMessage `json:"if"`
		ContinueOnError json.RawMessage `json:"continue-on-error"`
		Steps           []struct {
			Run             string          `json:"run"`
			If              json.RawMessage `json:"if"`
			ContinueOnError json.RawMessage `json:"continue-on-error"`
		} `json:"steps"`
	} `json:"jobs"`

	// YAML 1.1 reads a bare `on` as the boolean true, so the trigger block
	// arrives under the key "true". Reading it under "on" finds nothing and
	// says so about no workflow ever written, so both are taken.
	On          triggers `json:"on"`
	OnAsBoolean triggers `json:"true"`
}

// enforced reports whether a step or job still fails the workflow when its
// command fails.
//
// A gate is only a gate when it runs every time and its failure is fatal, so
// any if: at all -- not only a falsy one -- and any continue-on-error take it
// out of the count. Deciding which expressions are false is what let
// `if: ${{ false }}` through once; requiring that there be no condition
// cannot go wrong the same way. A gate that legitimately grows a condition
// fails this test, which is the point: someone should look.
//
// The values are raw JSON rather than any, because a bare `if:` is a present
// key holding null, which as an any is indistinguishable from no key at all.
func enforced(cond, continueOnError json.RawMessage) bool {
	if cond != nil {
		return false
	}
	switch strings.TrimSpace(string(continueOnError)) {
	case "", "false":
		return true
	}
	// Anything else, including the string "false". GitHub casts a non-empty
	// string to true, so a quoted "false" tolerates the failure -- and
	// guessing which spellings are falsy is the mistake that let
	// `if: ${{ false }}` through. A workflow writing this fails the test.
	return false
}

// workflowTriggers returns the events a workflow runs on.
func workflowTriggers(t *testing.T, path string) triggers {
	t.Helper()

	var wf workflow
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &wf); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	on := wf.On
	if on == nil {
		on = wf.OnAsBoolean
	}
	if on == nil {
		t.Fatalf("%s states no triggers, so nothing in it runs", path)
	}
	return on
}

// triggers accepts the three shapes GitHub allows for on:: a mapping of
// event to filters, a list of events, or one event on its own.
type triggers map[string]any

func (t *triggers) UnmarshalJSON(b []byte) error {
	var mapping map[string]any
	if err := json.Unmarshal(b, &mapping); err == nil {
		*t = mapping
		return nil
	}
	var events []string
	if err := json.Unmarshal(b, &events); err != nil {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		events = []string{one}
	}
	*t = triggers{}
	for _, event := range events {
		(*t)[event] = nil
	}
	return nil
}

// stringOrList accepts either shape a workflow may use.
type stringOrList []string

func (s *stringOrList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

// workflowCommands returns the run steps of every job, as YAML sees them --
// so a commented-out step is simply absent rather than something to detect.
func workflowCommands(t *testing.T, path string) []string {
	t.Helper()

	var wf workflow
	if err := yaml.Unmarshal([]byte(readFile(t, path)), &wf); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	var out []string
	for _, job := range wf.Jobs {
		if !enforced(job.If, job.ContinueOnError) {
			continue
		}
		for _, step := range job.Steps {
			if step.Run != "" && enforced(step.If, step.ContinueOnError) {
				out = append(out, step.Run)
			}
		}
	}
	return out
}

// invokesTarget reports whether a shell body asks make for the target.
//
// A line qualifies only when it starts with make and every word after it
// names a target of this Makefile. That settles the question with make's own
// list rather than by parsing shell, so prose cannot pass however it is
// quoted: `echo "gone; make vuln runs nightly"` does not start with make, and
// `make vuln runs nightly` offers "runs", which make does not know.
//
// It is deliberately narrow. `cd x && make vuln` and `make vuln || true` do
// not count -- the second because swallowing the failure is how you neuter a
// gate without removing it. A workflow that grows either shape fails this
// test loudly, which is the right way round for a gate.
//
// The one thing it cannot see is a heredoc whose body holds a line that is
// exactly `make vuln`. Writing that is no easier than deleting this test.
func invokesTarget(body string, targets map[string]bool, target string) bool {
	// The whole body has to be the one command. Judging each line separately
	// let a `make vuln` line inside a quoted message count, and let `set +e`
	// on the line above neuter the gate unseen -- and a step that runs a gate
	// has no reason to do anything else.
	line := strings.TrimSpace(body)
	words := strings.Fields(line)
	if strings.ContainsRune(line, '\n') || len(words) == 0 || words[0] != "make" {
		return false
	}
	asked := false
	for _, word := range words[1:] {
		if strings.HasPrefix(word, "#") {
			break // a trailing comment, not an argument
		}
		if !targets[word] {
			return false
		}
		asked = asked || word == target
	}
	return asked
}

// makeDatabase returns the rules make built, one line each.
//
// `make -np` does two things at once: -n echoes the recipe of the default
// goal and -p dumps the database. The echo comes first, and make strips the
// recipe's tab, so a recipe line that reads like a rule arrives at column
// zero and parses as one. The database starts at make's "# Files" banner;
// everything before it is the echo.
func makeDatabase(t *testing.T, dir string) []string {
	t.Helper()

	cmd := exec.Command("make", "-np", "--no-builtin-rules")
	cmd.Dir = dir
	// C, so the banner below is the English one make prints untranslated.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()

	// The last banner, not the first. What -n echoes is text the Makefile
	// being audited chooses, so it can contain the banner too; make's own
	// database is what it prints last.
	const banner = "\n# Files\n"
	at := strings.LastIndex(string(out), banner)
	if at < 0 {
		// A make that cannot do this says so, rather than the test claiming
		// the target does not exist.
		t.Fatalf("make -np printed no rule database: %v\n%s", err, out)
	}
	return strings.Split(string(out)[at+len(banner):], "\n")
}

// ruleLine splits a rule as -p prints it. A recipe line is indented and a
// comment starts with '#'; neither states a rule.
func ruleLine(line string) (target, rest string, ok bool) {
	if line == "" || line[0] == '\t' || line[0] == '#' || line[0] == ' ' {
		return "", "", false
	}
	target, rest, ok = strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return target, strings.TrimPrefix(rest, ":"), true // a :: rule
}

// targetVariable matches a target-specific variable, which -p prints beside
// the rule as "check: FOO = bar". The spaces around the operator are make's
// own, so a prerequisite whose name holds an '=' is not mistaken for one.
var targetVariable = regexp.MustCompile(`^\s+\S+\s+(::=|:=|\+=|\?=|!=|=)\s`)

// makePrerequisites asks make what a target depends on, rather than reading
// the Makefile.
func makePrerequisites(t *testing.T, dir, target string) []string {
	t.Helper()

	var deps []string
	known := false
	for _, line := range makeDatabase(t, dir) {
		name, rest, ok := ruleLine(line)
		if !ok || name != target {
			continue
		}
		// Said before the variable check, so a target that has one but no
		// prerequisites is reported as having none rather than as absent.
		known = true
		if targetVariable.MatchString(rest) {
			continue
		}
		// Every rule for the target, since :: may state them separately.
		deps = append(deps, strings.Fields(rest)...)
	}
	if !known {
		t.Fatalf("make does not know a target named %q", target)
	}
	return deps
}

// makeTargets returns every target make knows here, which is what tells a
// make invocation apart from a sentence about one.
func makeTargets(t *testing.T, dir string) map[string]bool {
	t.Helper()

	targets := map[string]bool{}
	for _, line := range makeDatabase(t, dir) {
		if name, rest, ok := ruleLine(line); ok && !targetVariable.MatchString(rest) {
			targets[name] = true
		}
	}
	return targets
}

// inlineCode matches a `span`; the documents use one for a version or an
// identifier, never for a section reference.
var inlineCode = regexp.MustCompile("`[^`\n]*`")

// mdLink matches a [text](target), whose target is never a citation.
var mdLink = regexp.MustCompile(`\[[^\]]*\]\([^)]*\)`)

// withoutCode blanks the fenced blocks, the inline code and the link targets,
// keeping every other line where it was.
func withoutCode(s string) string {
	var out []string
	fenced := false
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			out = append(out, "")
			continue
		}
		if fenced {
			out = append(out, "")
			continue
		}
		line = inlineCode.ReplaceAllString(line, " ")
		out = append(out, mdLink.ReplaceAllString(line, " "))
	}
	return strings.Join(out, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// Every reference to a spec section has to resolve.
//
// Renumbering one section and updating some of its citations is how this
// repository has broken twice: a heading was replaced and left the rule under
// it orphaned, and a later commit fixed the Makefile's citation but not the
// layout document's, in the same edit that claimed to fix exactly that.
func TestSpecCitationsResolve(t *testing.T) {
	headings := specHeadings(t)

	// The explicit form, "spec 08-testing.md 8.3.2", used from code, the
	// Makefile and the manuals. Anywhere in the repository.
	explicit := regexp.MustCompile(`([0-9]{2}-[a-z-]+\.md)\s+([0-9]+(?:\.[0-9]+)+)`)

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == "dist"):
			return fs.SkipDir
		case d.IsDir():
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".md", "":
		default:
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range explicit.FindAllStringSubmatch(string(body), -1) {
			doc, section := m[1], m[2]
			if headings[doc] == nil {
				t.Errorf("%s cites %s, which is not a spec document", path, doc)
				continue
			}
			if !headings[doc][section] {
				t.Errorf("%s cites %s %s, which is not a heading there", path, doc, section)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The bare form, （8.4.4）, only inside docs/spec/. There a bare number is
	// a spec section by convention; in the manuals it is the manual's own
	// numbering, and in prose it may be a version.
	bare := regexp.MustCompile(`[（(]([0-9]+(?:\.[0-9]+)+)[^）)]*[）)]`)

	specs, err := filepath.Glob("../docs/spec/*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range specs {
		text := withoutCode(readFile(t, path))
		for _, m := range bare.FindAllStringSubmatch(text, -1) {
			section := m[1]
			var found bool
			for _, sections := range headings {
				if sections[section] {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s cites section %s, which no spec document has", path, section)
			}
		}
	}
}

// specHeadings maps each spec document to the section numbers it declares.
func specHeadings(t *testing.T) map[string]map[string]bool {
	t.Helper()

	specs, err := filepath.Glob("../docs/spec/*.md")
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]map[string]bool{}
	for _, path := range specs {
		found := map[string]bool{}
		// Code first: a "# 8.9.9 …" inside a shell block is a comment, not a
		// heading, and registering it lets a dangling citation resolve.
		for _, line := range strings.Split(withoutCode(readFile(t, path)), "\n") {
			if !strings.HasPrefix(line, "#") {
				continue
			}
			if fields := strings.Fields(strings.TrimLeft(line, "#")); len(fields) > 0 {
				found[fields[0]] = true
			}
		}
		out[filepath.Base(path)] = found
	}
	return out
}

// The shell-level judgement decides whether a gate reads as run. YAML settles
// the step's shape; this settles what the step's body does, and it has been
// wrong in both directions before.
func TestInvokesTarget(t *testing.T) {
	// A fixed set, so this holds the rule rather than the Makefile.
	targets := map[string]bool{
		"vuln": true, "tidy": true, "test": true, "test-integration": true,
		"cover": true, "cover-html": true, "build": true,
	}
	for _, tt := range []struct {
		name, body string
		want       bool
	}{
		{"plain", "make vuln", true},
		{"among other targets", "make tidy vuln test", true},
		{"with a trailing comment", "make vuln # nightly as well", true},
		{"prose that names it", `make vuln runs nightly now`, false},
		{"printed with a quoted separator", `echo "gone; make vuln runs nightly"`, false},
		{"printed with a quoted ampersand", `echo "removed & make vuln is gone"`, false},
		{"printed as a bare word", "echo vuln", false},
		{"in a trailing comment", "go build ./...   # make vuln disabled", false},
		{"failure swallowed", "make vuln || true", false},
		{"a longer target that starts the same", "make vulnerable", false},
		{"no target at all", "make", false},
		{"not there", "go build ./...", false},
		{"quoted across lines", "echo \"dropped, because\nmake vuln\ntook too long\"", false},
		{"with the shell's -e turned off", "set +e\nmake vuln\necho done", false},
		{"written into a file", "cat > NOTE <<'EOF'\nmake vuln\nEOF", false},
		{"a second line holding only target names", "make tidy\nvuln", false},
		{"on a later line", "go build ./...\nmake vuln", false},
	} {
		if got := invokesTarget(tt.body, targets, "vuln"); got != tt.want {
			t.Errorf("invokesTarget(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}

	// A target whose name is a prefix of another must not answer for it.
	for _, tt := range []struct{ body, target string }{
		{"make test-integration", "test"},
		{"make cover-html", "cover"},
	} {
		if invokesTarget(tt.body, targets, tt.target) {
			t.Errorf("invokesTarget(%q, %q) = true, want false", tt.body, tt.target)
		}
	}
}

// A gate counts only when it runs every time and its failure is fatal.
func TestEnforced(t *testing.T) {
	raw := func(j string) json.RawMessage {
		if j == "" {
			return nil // the key is not there at all
		}
		return json.RawMessage(j)
	}
	for _, tt := range []struct {
		name, cond, continueOn string
		want                   bool
	}{
		{"nothing set", "", "", true},
		{"failure fatal, said out loud", "", "false", true},
		{"failure fatal, as a string", "", `"false"`, false},
		{"failure allowed", "", "true", false},
		{"failure allowed, as a string", "", `"true"`, false},
		{"failure allowed by an expression", "", `"${{ github.event_name }}"`, false},
		{"switched off", "false", "", false},
		{"switched off as a string", `"false"`, "", false},
		{"switched off by an expression", `"${{ false }}"`, "", false},
		{"a bare if:, which YAML reads as null", "null", "", false},
		{"conditional on the event", `"${{ github.event_name == 'push' }}"`, "", false},
		{"always, said out loud", "true", "", false},
	} {
		if got := enforced(raw(tt.cond), raw(tt.continueOn)); got != tt.want {
			t.Errorf("enforced(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// -n echoes the default goal's recipe before -p prints the database, with
// the recipe's tab stripped. A recipe line that reads like a rule therefore
// arrives at column zero, and must not be mistaken for one.
func TestMakeDatabaseIgnoresTheDryRunEcho(t *testing.T) {
	dir := t.TempDir()
	probe := "build:\n\tcheck: tidy strict-lint test\n\ncheck: nothing-real\n\nnothing-real:\n\t@true\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := makePrerequisites(t, dir, "check"); !slices.Equal(got, []string{"nothing-real"}) {
		t.Errorf("check depends on %v, want the rule make built, not the line it echoed", got)
	}
	if targets := makeTargets(t, dir); targets["strict-lint"] {
		t.Error("makeTargets took a word from the echoed recipe for a target")
	}
}

// The echo is text the audited Makefile chooses, so it can hold the banner
// the cut looks for. make's own database is the last one printed.
func TestMakeDatabaseIgnoresAForgedBanner(t *testing.T) {
	dir := t.TempDir()
	probe := "build:\n\ttrue first\n\t# Files\n\tcheck: tidy strict-lint test\n\ncheck: nothing-real\n\nnothing-real:\n\t@true\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := makePrerequisites(t, dir, "check"); !slices.Equal(got, []string{"nothing-real"}) {
		t.Errorf("check depends on %v, want the rule make built, not the forged database", got)
	}
}

// makePrerequisites reads make's database, which make prints after the text
// -n echoes; ruleLine and targetVariable are what keep the two apart.
func TestRuleLine(t *testing.T) {
	for _, tt := range []struct {
		line, target, rest string
		ok                 bool
	}{
		{"check: tidy strict-lint test", "check", " tidy strict-lint test", true},
		{"check:: tidy", "check", " tidy", true},
		{"check:", "check", "", true},
		{"\tcheck: tidy", "", "", false},
		{"# check: tidy", "", "", false},
		{"  check: tidy", "", "", false},
		{"not a rule", "", "", false},
		{"", "", "", false},
	} {
		target, rest, ok := ruleLine(tt.line)
		if ok != tt.ok || target != tt.target || rest != tt.rest {
			t.Errorf("ruleLine(%q) = %q, %q, %v, want %q, %q, %v",
				tt.line, target, rest, ok, tt.target, tt.rest, tt.ok)
		}
	}

	for _, tt := range []struct {
		rest string
		want bool
	}{
		{" FOO = bar", true},
		{" FOO := bar", true},
		{" FOO += bar", true},
		{" FOO ?= bar", true},
		{" tidy strict-lint test", false},
		{" out=1.txt tidy", false}, // a prerequisite, not an assignment
		{"", false},
	} {
		if got := targetVariable.MatchString(tt.rest); got != tt.want {
			t.Errorf("targetVariable(%q) = %v, want %v", tt.rest, got, tt.want)
		}
	}
}

// withoutCode still needs holding: the citation checker depends on it, and
// blanking too little lets a shell comment register as a heading while
// blanking too much hides a real citation.
func TestWithoutCode(t *testing.T) {
	got := withoutCode("```bash\n# 8.9.9 a shell comment\n```\n## 8.1 real\n")
	if strings.Contains(got, "8.9.9") {
		t.Errorf("withoutCode = %q, want a fenced line blanked", got)
	}
	if !strings.Contains(got, "8.1") {
		t.Errorf("withoutCode = %q, want a real heading kept", got)
	}
	if strings.Contains(withoutCode("see `1.0` here"), "1.0") {
		t.Error("inline code was not blanked")
	}
	if strings.Contains(withoutCode("[x](3.99)"), "3.99") {
		t.Error("a link target was not blanked")
	}

	// A heading line with no text must not take the package down.
	if fields := strings.Fields(strings.TrimLeft("#  ", "#")); len(fields) != 0 {
		t.Errorf("fields = %v, want none", fields)
	}
}

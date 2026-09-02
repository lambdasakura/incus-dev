package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/cli"
	"github.com/lambdasakura/incus-dev/internal/config"
)

// skillDirs lists the skills under skills/.
//
// There is one per language, and every one goes through the same checks.
func skillDirs(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join("..", "skills"))
	if err != nil {
		t.Fatalf("read skills: %v", err)
	}

	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join("..", "skills", e.Name()))
		}
	}
	if len(dirs) == 0 {
		t.Fatal("skills/ holds no skill at all")
	}
	return dirs
}

// The template a skill hands out is always valid. A broken template given to an
// agent turns straight into a broken dev.yml.
func TestSkillTemplateIsValid(t *testing.T) {
	for _, dir := range skillDirs(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			path := filepath.Join(dir, "templates", "dev.yml")

			if _, err := config.Load(path); err != nil {
				t.Errorf("config.Load() error = %v", err)
			}
		})
	}
}

// codeIn returns the code in a Markdown document, one span or block line per
// line, with the prose around it left out.
func codeIn(md string) string {
	fenced := regexp.MustCompile("(?s)```[^\n]*\n(.*?)```")
	inline := regexp.MustCompile("`([^`\n]*)`")

	var b strings.Builder
	for _, m := range fenced.FindAllStringSubmatch(md, -1) {
		b.WriteString(m[1] + "\n")
	}
	for _, m := range inline.FindAllStringSubmatch(fenced.ReplaceAllString(md, ""), -1) {
		b.WriteString(m[1] + "\n")
	}
	return b.String()
}

// The commands a skill points at exist. This is what catches a skill left
// behind when the CLI changes.
func TestSkillReferencesExistingCommands(t *testing.T) {
	root := filepath.Join("..", "skills")

	// Taken from the command tree, not written out here: a hand-kept list
	// would have to be updated alongside the CLI, which is the very thing this
	// test exists to catch.
	known := map[string]bool{}
	for _, c := range cli.NewRootCommand("test").Commands() {
		known[c.Name()] = true
	}
	// Pick up the `idev <subcommand>` shape; `idev --version` and the like are out
	// of scope.
	//
	// Only what a code span or a fenced block starts with counts as pointing
	// at a command. Prose ("idev does not create them") and a quoted error
	// ("... is not managed by idev for project ...") mention idev without
	// naming a subcommand.
	command := regexp.MustCompile(`(?m)^\s*idev ([a-z][a-z-]*)`)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path) //nolint:gosec // the walk is over a fixed path inside the repository
		if err != nil {
			return err
		}
		for _, m := range command.FindAllStringSubmatch(codeIn(withoutFrontmatter(string(body))), -1) {
			if !known[m[1]] {
				t.Errorf("%s: points at %q, which does not exist", path, "idev "+m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// withoutFrontmatter returns the body with the YAML frontmatter removed.
func withoutFrontmatter(text string) string {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return text
	}
	if _, body, found := strings.Cut(rest, "\n---\n"); found {
		return body
	}
	return text
}

// A skill's entry point, SKILL.md, is in a shape an agent can read.
//
// name identifies the skill, so it matches the directory name and is unique.
func TestSkillHasFrontmatter(t *testing.T) {
	seen := map[string]string{}

	for _, dir := range skillDirs(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
			if err != nil {
				t.Fatalf("read SKILL.md: %v", err)
			}

			text := string(body)
			if !strings.HasPrefix(text, "---\n") {
				t.Fatal("SKILL.md does not begin with frontmatter")
			}
			front, _, found := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
			if !found {
				t.Fatal("the frontmatter is never closed")
			}
			for _, key := range []string{"name:", "description:"} {
				if !strings.Contains(front, key) {
					t.Errorf("the frontmatter has no %s", key)
				}
			}

			name := skillName(front)
			if want := filepath.Base(dir); name != want {
				t.Errorf("name = %q, want it to match the directory name %q", name, want)
			}
			if other, dup := seen[name]; dup {
				t.Errorf("name %q collides with %s", name, other)
			}
			seen[name] = dir
		})
	}
}

// skillName pulls the value of name out of the frontmatter, or the empty string.
func skillName(front string) string { return skillField(front, "name:") }

// skillField pulls a frontmatter value out, or the empty string.
func skillField(front, key string) string {
	for _, line := range strings.Split(front, "\n") {
		if rest, ok := strings.CutPrefix(line, key); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// The two skills, and the two manuals, hold the same set of files.
//
// A page added to one language and forgotten in the other is the way these
// pairs drift (CLAUDE.md, "利用者向け文書は英語と日本語の両方を保つ").
func TestTranslationsHoldTheSameFiles(t *testing.T) {
	for _, pair := range []struct{ en, ja string }{
		{filepath.Join("..", "skills", "incus-dev"), filepath.Join("..", "skills", "incus-dev-ja")},
		{filepath.Join("..", "docs", "manual"), filepath.Join("..", "docs", "manual", "ja")},
	} {
		t.Run(filepath.Base(pair.en), func(t *testing.T) {
			en, ja := relativeFiles(t, pair.en), relativeFiles(t, pair.ja)

			for name := range en {
				if !ja[name] {
					t.Errorf("%s has %s, %s does not", pair.en, name, pair.ja)
				}
			}
			for name := range ja {
				if !en[name] {
					t.Errorf("%s has %s, %s does not", pair.ja, name, pair.en)
				}
			}
		})
	}
}

// relativeFiles lists the files under dir, excluding the nested ja/ directory.
func relativeFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && d.Name() == "ja" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// Every manual page links to its counterpart in the other language.
//
// It is how a reader switches, and one page was missing the link while the
// other fifteen carried it.
func TestManualPagesLinkToTheOtherLanguage(t *testing.T) {
	for _, tt := range []struct{ dir, want string }{
		{filepath.Join("..", "docs", "manual"), "日本語版"},
		{filepath.Join("..", "docs", "manual", "ja"), "English version"},
	} {
		for name := range relativeFiles(t, tt.dir) {
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(tt.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("%s: no link to the other language (want %q)", filepath.Join(tt.dir, name), tt.want)
			}
		}
	}
}

// devSkillsDir holds the skills for working *on* this repository, as opposed
// to skills/, which is shipped to users of idev.
const devSkillsDir = "../.claude/skills"

// The developer skills are read by whoever picks up the next round of work, so
// a broken frontmatter or a dead link costs exactly when it is least noticed.
func TestDeveloperSkillsAreWellFormed(t *testing.T) {
	entries, err := os.ReadDir(devSkillsDir)
	if err != nil {
		t.Fatalf("read %s: %v", devSkillsDir, err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join(devSkillsDir, e.Name(), "SKILL.md")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			front, _, ok := strings.Cut(strings.TrimPrefix(string(body), "---\n"), "\n---")
			if !ok {
				t.Fatal("SKILL.md does not begin with frontmatter")
			}

			name := skillName(front)
			if name != e.Name() {
				t.Errorf("name = %q, want the directory name %q", name, e.Name())
			}
			if seen[name] {
				t.Errorf("two skills are named %q", name)
			}
			seen[name] = true

			// The description is what decides whether the skill is reached
			// for at all, so an empty one makes the skill invisible.
			if d := skillField(front, "description:"); len(d) < 40 {
				t.Errorf("description = %q, want it to say when to use the skill", d)
			}

			// Links into the repository have to resolve, since these are read
			// while working in it.
			for _, target := range markdownLinks(string(body)) {
				if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "#") {
					continue
				}
				if _, err := os.Stat(filepath.Join("..", target)); err != nil {
					t.Errorf("link %q does not resolve from the repository root", target)
				}
			}
		})
	}

	if len(seen) == 0 {
		t.Errorf("%s holds no skill at all", devSkillsDir)
	}
}

// CLAUDE.md points at the developer skills; the pointers have to resolve.
func TestClaudeMdLinksToTheDeveloperSkills(t *testing.T) {
	body, err := os.ReadFile("../CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}

	var linked int
	for _, target := range markdownLinks(string(body)) {
		if !strings.HasPrefix(target, ".claude/skills/") {
			continue
		}
		linked++
		if _, err := os.Stat(filepath.Join("..", target)); err != nil {
			t.Errorf("CLAUDE.md links %q, which does not resolve", target)
		}
	}
	if linked == 0 {
		t.Error("CLAUDE.md links no developer skill, so nothing will find them")
	}
}

// markdownLinks returns the targets of the [text](target) links in body.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func markdownLinks(body string) []string {
	var out []string
	for _, m := range markdownLink.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

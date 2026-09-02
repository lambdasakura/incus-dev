package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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

	known := map[string]bool{
		"up": true, "provision": true, "shell": true, "exec": true,
		"status": true, "validate": true, "destroy": true, "rebuild": true,
		"snapshot": true, "completion": true,
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
func skillName(front string) string {
	for _, line := range strings.Split(front, "\n") {
		if rest, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// Package test checks the repository's overall structure and its examples.
package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/lambdasakura/incus-dev/internal/config"
)

// The samples under examples/ are always valid (spec 08-testing.md 8.4).
func TestExamplesAreValid(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "examples"))
	if err != nil {
		t.Fatalf("read examples: %v", err)
	}

	var checked int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			path := filepath.Join("..", "examples", e.Name(), ".incus-dev", "dev.yml")
			if _, err := config.Load(path); err != nil {
				t.Errorf("config.Load() error = %v", err)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Error("no example was found")
	}
}

// The sample configurations are read by users of any language, and there is
// only one copy of them, so their comments are English. The Markdown beside
// them is the part that exists in both languages (README.md / README.ja.md).
func TestExamplesAreASCII(t *testing.T) {
	err := filepath.WalkDir(filepath.Join("..", "examples"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "../"))
		for i, line := range strings.Split(string(data), "\n") {
			for _, r := range line {
				if r > unicode.MaxASCII {
					t.Errorf("%s:%d contains non-ASCII %q. Sample configurations are written in English", rel, i+1, r)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// The reference sample in the manual is a valid dev.yml.
//
// It is what a user copies from, and it drifted: instance.type stayed in it
// after the setting was removed, so the sample no longer parsed. Both
// languages carry their own copy, so both are checked.
func TestManualSampleIsValid(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "docs", "manual", "04-dev-yml.md"),
		filepath.Join("..", "docs", "manual", "ja", "04-dev-yml.md"),
	} {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sample := firstYAMLBlock(t, string(body))

			root := t.TempDir()
			writeUnder(t, root, ".incus-dev/dev.yml", sample)
			writeUnder(t, root, ".incus-dev/ansible/site.yml", "---\n")
			writeUnder(t, root, ".incus-dev/scripts/setup.sh", "#!/bin/sh\n")

			if _, err := config.Load(filepath.Join(root, ".incus-dev", "dev.yml")); err != nil {
				t.Errorf("config.Load() error = %v", err)
			}
		})
	}
}

// firstYAMLBlock returns the contents of the first ```yaml fence.
func firstYAMLBlock(t *testing.T, md string) string {
	t.Helper()

	m := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindStringSubmatch(md)
	if m == nil {
		t.Fatal("no yaml block found")
	}
	return m[1]
}

// writeUnder writes a file below root, creating the directories it needs.
func writeUnder(t *testing.T, root, rel, body string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// removedAnsibleSettings are ansible.cfg settings that current Ansible
// refuses outright, keyed to what replaced them.
//
// idev hands the example's ansible.cfg to ansible-playbook as ANSIBLE_CONFIG,
// so one of these in a shipped example fails the run with a message about a
// callback plugin -- nothing a reader would trace back to the example.
var removedAnsibleSettings = map[string]string{
	// Removed in community.general 12.0.0.
	"stdout_callback = yaml": "result_format = yaml",
}

// iniBlock matches a fenced ini block, which is how the manual shows a config
// the reader is meant to copy.
var iniBlock = regexp.MustCompile("(?s)```ini\\n(.*?)```")

func TestExamplesAvoidRemovedAnsibleSettings(t *testing.T) {
	// Only what a reader copies: an ansible.cfg, and the fenced ini blocks in
	// the documentation. Prose has to be able to name a broken setting in
	// order to warn about it -- show a counter-example in a ```text block.
	check := func(t *testing.T, path, body string) {
		t.Helper()

		for removed, replacement := range removedAnsibleSettings {
			if strings.Contains(body, removed) {
				t.Errorf("%s uses %q, which current Ansible rejects; use %q",
					path, removed, replacement)
			}
		}
	}

	for _, root := range []string{"../examples", "../docs/manual", "../docs/spec"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			switch {
			case d.Name() == "ansible.cfg":
				check(t, path, string(body))
			case strings.HasSuffix(path, ".md"):
				for _, m := range iniBlock.FindAllStringSubmatch(string(body), -1) {
					check(t, path, m[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

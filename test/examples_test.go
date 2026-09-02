// Package test checks the repository's overall structure and its examples.
package test

import (
	"io/fs"
	"os"
	"path/filepath"
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

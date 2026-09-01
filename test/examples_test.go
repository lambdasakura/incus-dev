// Package test checks the repository's overall structure and its examples.
package test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lambdasakura/incus-devkit/internal/config"
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

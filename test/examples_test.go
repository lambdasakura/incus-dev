// Package test はリポジトリ全体の構造とサンプルを検証する。
package test

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// examples/ 配下のサンプルが常に妥当であること（仕様 08-testing.md 8.4）
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
		t.Error("examples が1つも見つからない")
	}
}

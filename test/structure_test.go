package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// devkitは環境固有の資産を持たない（REQ-007、仕様 08-testing.md 8.2）
func TestNoEnvironmentSpecificAssets(t *testing.T) {
	forbidden := []string{
		"ansible",          // 共通Playbook / Role
		"profiles",         // 共通Incus Profile
		"requirements.yml", // 共通collection定義
		"roles",
	}
	for _, name := range forbidden {
		t.Run(name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join("..", name)); err == nil {
				t.Errorf("%s はdevkitに存在してはならない（REQ-007）。"+
					"環境固有の内容はプロジェクトの .incus-dev/ に属する", name)
			}
		})
	}
}

// バイナリへ同梱するのはJSON Schemaのみであること（仕様 02-repository-layout.md 2.4）
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
		// テストファイルはバイナリに含まれないため対象外。
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
		t.Errorf("go:embed を使うファイル = %v, want %v\n"+
			"devkitが同梱してよいのはJSON Schemaのみ（REQ-007）", files, want)
	}
}

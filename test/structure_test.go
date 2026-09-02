package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// devkitは環境固有の資産を持たない（REQ-007、仕様 08-testing.md 8.2）
//
// リポジトリ全体を走査する。examples/ と test/ は利用者向けサンプルと
// テストフィクスチャなので対象外（仕様 02-repository-layout.md 2.3）。
func TestNoEnvironmentSpecificAssets(t *testing.T) {
	forbidden := map[string]string{
		"ansible":          "共通PlaybookやRole",
		"profiles":         "共通Incus Profile",
		"roles":            "共通Ansible Role",
		"requirements.yml": "共通collection定義",
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
			t.Errorf("%s が存在する（%s）。REQ-007により、環境固有の内容は "+
				"devkitではなくプロジェクトの .incus-dev/ に属する", rel, reason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// 実装コードにOS固有のコマンドが紛れ込んでいないこと（REQ-007）。
//
// ファイル名の検査だけでは「.go の中へ直接書き足す」形の違反を防げないため、
// パッケージマネージャの呼び出しを本文で検査する。
func TestNoOSSpecificCommandsInImplementation(t *testing.T) {
	// 仕様 06-provisioning.md 6.3.2 が認める唯一の例外。
	allowed := map[string]string{
		"internal/provision/provision.go": "既定bootstrap（上書き・無効化が可能）",
	}
	managers := []string{"apt-get", "apt install", "dnf ", "yum ", "apk add", "pacman -S", "zypper "}

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// examples/ と docs/ と test/ は利用者向けの例なので対象外。
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
				t.Logf("%s: %q（%s）", rel, m, reason)
				continue
			}
			t.Errorf("%s が %q を含む。REQ-007により、OS固有の手順は "+
				"devkitではなくプロジェクトの .incus-dev/ に属する", rel, m)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
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

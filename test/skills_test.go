package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// スキルが配る雛形が常に妥当であること。
// 壊れた雛形をエージェントへ渡すと、そのまま壊れた dev.yml が生まれる。
func TestSkillTemplateIsValid(t *testing.T) {
	path := filepath.Join("..", "skills", "incus-devkit", "templates", "dev.yml")

	if _, err := config.Load(path); err != nil {
		t.Errorf("config.Load() error = %v", err)
	}
}

// スキルが案内するコマンドが実在すること。
// CLIを変えたときに追従漏れへ気付けるようにする。
func TestSkillReferencesExistingCommands(t *testing.T) {
	root := filepath.Join("..", "skills", "incus-devkit")

	known := map[string]bool{
		"up": true, "provision": true, "shell": true, "exec": true,
		"status": true, "validate": true, "destroy": true, "rebuild": true,
		"snapshot": true, "completion": true,
	}
	// `idev <サブコマンド>` の形を拾う（`idev --version` などは対象外）。
	command := regexp.MustCompile(`idev ([a-z][a-z-]*)`)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path) //nolint:gosec // 走査対象はリポジトリ内の固定パス
		if err != nil {
			return err
		}
		for _, m := range command.FindAllStringSubmatch(string(body), -1) {
			if !known[m[1]] {
				t.Errorf("%s: 存在しないコマンド %q を案内している", path, "idev "+m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// スキルの入口（SKILL.md）が、エージェントが読める形であること。
func TestSkillHasFrontmatter(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "skills", "incus-devkit", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}

	text := string(body)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatal("SKILL.md がfrontmatterで始まっていない")
	}
	front, _, found := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !found {
		t.Fatal("frontmatterが閉じていない")
	}
	for _, key := range []string{"name:", "description:"} {
		if !strings.Contains(front, key) {
			t.Errorf("frontmatterに %s が無い", key)
		}
	}
}

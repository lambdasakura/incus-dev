package test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// skillDirs は skills/ 配下のスキル一覧を返す。
//
// 言語別に複数のスキルがあるため、どれも同じ検査を通す。
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
		t.Fatal("skills/ にスキルが1つも無い")
	}
	return dirs
}

// スキルが配る雛形が常に妥当であること。
// 壊れた雛形をエージェントへ渡すと、そのまま壊れた dev.yml が生まれる。
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

// スキルが案内するコマンドが実在すること。
// CLIを変えたときに追従漏れへ気付けるようにする。
func TestSkillReferencesExistingCommands(t *testing.T) {
	root := filepath.Join("..", "skills")

	known := map[string]bool{
		"up": true, "provision": true, "shell": true, "exec": true,
		"status": true, "validate": true, "destroy": true, "rebuild": true,
		"snapshot": true, "completion": true,
	}
	// `idev <サブコマンド>` の形を拾う（`idev --version` などは対象外）。
	command := regexp.MustCompile(`idev ([a-z][a-z-]*)`)

	// frontmatterは散文なので対象外。英語では "the idev command" のような
	// 言い回しが混ざり、コマンドの案内と区別できない。

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		body, err := os.ReadFile(path) //nolint:gosec // 走査対象はリポジトリ内の固定パス
		if err != nil {
			return err
		}
		for _, m := range command.FindAllStringSubmatch(withoutFrontmatter(string(body)), -1) {
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

// withoutFrontmatter はYAML frontmatterを取り除いた本文を返す。
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

// スキルの入口（SKILL.md）が、エージェントが読める形であること。
//
// name はスキルの識別子なので、ディレクトリ名と一致し、かつ重複しないこと。
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

			name := skillName(front)
			if want := filepath.Base(dir); name != want {
				t.Errorf("name = %q, ディレクトリ名 %q と一致すること", name, want)
			}
			if other, dup := seen[name]; dup {
				t.Errorf("name %q が %s と重複している", name, other)
			}
			seen[name] = dir
		})
	}
}

// skillName はfrontmatterから name の値を取り出す。無ければ空文字列。
func skillName(front string) string {
	for _, line := range strings.Split(front, "\n") {
		if rest, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

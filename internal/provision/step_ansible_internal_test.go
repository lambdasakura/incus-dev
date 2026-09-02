package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

func TestWriteYAMLAndJSON(t *testing.T) {
	dir := t.TempDir()

	t.Run("YAMLを書き出す", func(t *testing.T) {
		path := filepath.Join(dir, "out.yml")
		if err := writeYAML(path, map[string]any{"key": "value"}); err != nil {
			t.Fatalf("writeYAML() error = %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "key: value") {
			t.Errorf("content = %q", data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("permission = %o, want 600", perm)
		}
	})

	t.Run("JSONを書き出す", func(t *testing.T) {
		path := filepath.Join(dir, "out.json")
		if err := writeJSON(path, map[string]any{"key": "value"}); err != nil {
			t.Fatalf("writeJSON() error = %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"key":"value"}` {
			t.Errorf("content = %q", data)
		}
	})

	t.Run("変換できない値", func(t *testing.T) {
		if err := writeYAML(filepath.Join(dir, "bad.yml"), make(chan int)); err == nil {
			t.Error("writeYAML() = nil error, want error")
		}
		if err := writeJSON(filepath.Join(dir, "bad.json"), make(chan int)); err == nil {
			t.Error("writeJSON() = nil error, want error")
		}
	})

	t.Run("書き込めないパス", func(t *testing.T) {
		bad := filepath.Join(dir, "no-such-dir", "out.yml")
		if err := writeYAML(bad, map[string]any{}); err == nil {
			t.Error("writeYAML() = nil error, want error")
		}
		if err := writeJSON(bad, map[string]any{}); err == nil {
			t.Error("writeJSON() = nil error, want error")
		}
	})
}

func TestResolvePath(t *testing.T) {
	if got := resolve("/root", "/abs/path"); got != "/abs/path" {
		t.Errorf("resolve() = %q, 絶対パスはそのまま返すこと", got)
	}
	if got := resolve("/root", "rel"); got != "/root/rel" {
		t.Errorf("resolve() = %q", got)
	}
}

func TestOrDefault(t *testing.T) {
	if got := orDefault("", "fallback"); got != "fallback" {
		t.Errorf("orDefault() = %q", got)
	}
	if got := orDefault("value", "fallback"); got != "value" {
		t.Errorf("orDefault() = %q", got)
	}
}

// 一時ディレクトリを作れない場合はエラーになる
func TestExecAnsibleFailsWhenTempDirUnavailable(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-dir"))

	e := &Executor{Runner: &runnertest.Fake{}}
	err := e.execAnsible(t.Context(), &config.AnsibleStep{Playbook: "site.yml"}, Env{ProjectRoot: t.TempDir()})

	if err == nil || !strings.Contains(err.Error(), "temporary directory") {
		t.Errorf("error = %v", err)
	}
}

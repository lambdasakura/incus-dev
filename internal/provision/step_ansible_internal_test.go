package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yamlv2 "go.yaml.in/yaml/v2"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/runner/runnertest"
)

func TestWriteYAMLAndJSON(t *testing.T) {
	dir := t.TempDir()

	t.Run("writes YAML", func(t *testing.T) {
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

	t.Run("writes JSON", func(t *testing.T) {
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

	t.Run("a value that cannot be converted", func(t *testing.T) {
		if err := writeYAML(filepath.Join(dir, "bad.yml"), make(chan int)); err == nil {
			t.Error("writeYAML() = nil error, want error")
		}
		if err := writeJSON(filepath.Join(dir, "bad.json"), make(chan int)); err == nil {
			t.Error("writeJSON() = nil error, want error")
		}
	})

	t.Run("a path that cannot be written", func(t *testing.T) {
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
		t.Errorf("resolve() = %q, want an absolute path returned as it is", got)
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

// Being unable to create the temporary directory is an error.
func TestExecAnsibleFailsWhenTempDirUnavailable(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-dir"))

	// ansible-doc answering as it does on a host that has the plugin, so the
	// failure under test is the temporary directory and nothing else.
	e := &Executor{Runner: &runnertest.Fake{Stdout: map[string]string{
		"ansible-doc": "> COMMUNITY.GENERAL.INCUS    (connection plugin)\n",
	}}}
	err := e.execAnsible(t.Context(), &config.AnsibleStep{Playbook: "site.yml"}, Env{ProjectRoot: t.TempDir()})

	if err == nil || !strings.Contains(err.Error(), "temporary directory") {
		t.Errorf("error = %v", err)
	}
}

// A secret must reach the playbook as it was written.
//
// Ansible templates every value it reads from --extra-vars, so a token that
// happens to contain {{ was evaluated: "abc{{ 1 + 1 }}def" arrived as
// "abc2def", and one naming an undefined variable aborted the play with an
// error about a variable the user never wrote. The same secret reaches a run
// step byte for byte, so the two ways of injecting it disagreed.
func TestSecretsAreWrittenUntemplated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.yml")

	secrets := map[string]string{
		"TEMPLATE":  "abc{{ 1 + 1 }}def",
		"UNDEFINED": "a{{ oops }}b",
		"QUOTES":    `a"b'c`,
		"NEWLINE":   "line1\nline2",
		"BACKSLASH": `back\slash`,
		"UNICODE":   "パス",
	}
	if err := writeSecrets(path, secrets); err != nil {
		t.Fatalf("writeSecrets() error = %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name := range secrets {
		if !strings.Contains(string(body), name+": !unsafe ") {
			t.Errorf("%s is not tagged !unsafe:\n%s", name, body)
		}
	}
	// The raw braces must survive into the file; escaping them would change
	// the value just as templating does.
	if !strings.Contains(string(body), `abc{{ 1 + 1 }}def`) {
		t.Errorf("the value was rewritten:\n%s", body)
	}

	// And it has to be YAML that reads back as what went in.
	var got map[string]string
	if err := yamlv2.Unmarshal(body, &got); err != nil {
		t.Fatalf("the file is not YAML: %v\n%s", err, body)
	}
	for name, want := range secrets {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
}

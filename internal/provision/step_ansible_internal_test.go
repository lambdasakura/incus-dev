package provision

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

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
// Only real ansible-playbook can say whether it does. Two earlier shapes both
// passed a Go parser and corrupted the value in Ansible: plain JSON templates
// it, and !unsafe YAML re-reads its type. A Go YAML library reads both back
// correctly, so a test written against one guards nothing.
func TestSecretsSurviveRealAnsible(t *testing.T) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("skipping: ansible-playbook is not installed")
	}

	secrets := map[string]string{
		"LEADING_ZERO": "0123456",         // resolves to an octal int if the type is re-read
		"DECIMAL":      "1.20",            // loses its trailing zero as a float
		"OFF":          "Off",             // a YAML boolean
		"EMPTY":        "",                // null
		"DATE":         "2026-09-01",      // a date object
		"TEMPLATE":     "abc{{ 1+1 }}def", // evaluated if it is a template
		"UNDEFINED":    "a{{ oops }}b",    // aborts the play if it is
		"QUOTES":       `a"b'c`,
		"NEWLINE":      "line1\nline2",
		"UNICODE":      "パス",
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := writeSecrets(path, secrets); err != nil {
		t.Fatalf("writeSecrets() error = %v", err)
	}

	// Ansible prints each value with a marker around it, so trailing spaces
	// and emptiness are visible in the output.
	playbook := filepath.Join(dir, "check.yml")
	body := "- hosts: localhost\n  gather_facts: false\n  tasks:\n" +
		"    - debug:\n        msg: \"{{ item }}=[{{ lookup('vars', item) }}]\"\n" +
		"      loop: [" + quotedNames(secrets) + "]\n"
	if err := os.WriteFile(playbook, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("ansible-playbook", "-i", "localhost,", "-c", "local",
		"--extra-vars=@"+path, playbook)
	cmd.Env = append(os.Environ(), "ANSIBLE_NOCOLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ansible-playbook: %v\n%s", err, out)
	}

	for name, want := range secrets {
		// Ansible prints the message as a JSON string, so the expected value
		// is escaped the same way before looking for it.
		if !strings.Contains(string(out), asJSONFragment(t, name+"=["+want+"]")) {
			t.Errorf("%s did not arrive as %q; ansible said:\n%s", name, want, out)
		}
	}
}

// quotedNames renders the variable names as a YAML list of quoted strings.
// Unquoted, a name like OFF is read as a boolean by the playbook itself.
func quotedNames(secrets map[string]string) string {
	names := slices.Sorted(maps.Keys(secrets))
	for i, name := range names {
		names[i] = strconv.Quote(name)
	}
	return strings.Join(names, ", ")
}

// asJSONFragment renders text as it appears inside a JSON string, which is how
// ansible-playbook prints a debug message.
func asJSONFragment(t *testing.T, text string) string {
	t.Helper()

	var sb strings.Builder
	encoder := json.NewEncoder(&sb)
	encoder.SetEscapeHTML(false) // ansible does not escape them either
	if err := encoder.Encode(text); err != nil {
		t.Fatal(err)
	}
	return strings.Trim(strings.TrimSpace(sb.String()), `"`)
}

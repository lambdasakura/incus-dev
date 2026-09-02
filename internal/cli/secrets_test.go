package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/config"
)

func TestResolveSecrets(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := mustParse(t, planBase+`
secrets:
  FROM_ENV:
    env: HOST_TOKEN
  FROM_FILE:
    file: `+keyPath+`
`)
	env := map[string]string{"HOST_TOKEN": "env-secret"}

	got, err := resolveSecrets(cfg, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	if err != nil {
		t.Fatalf("resolveSecrets() error = %v", err)
	}

	want := map[string]string{"FROM_ENV": "env-secret", "FROM_FILE": "file-secret"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("resolveSecrets() mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveSecretsWithoutDeclaration(t *testing.T) {
	got, err := resolveSecrets(mustParse(t, planBase), os.LookupEnv)
	if err != nil || got != nil {
		t.Errorf("resolveSecrets() = %v, %v", got, err)
	}
}

// Whatever cannot be read is reported together, naming what is missing.
func TestResolveSecretsReportsMissing(t *testing.T) {
	cfg := mustParse(t, planBase+`
secrets:
  A:
    env: MISSING_A
  B:
    file: /no/such/file
  C:
    env: PRESENT
`)
	_, err := resolveSecrets(cfg, func(k string) (string, bool) {
		return "value", k == "PRESENT"
	})
	if err == nil {
		t.Fatal("resolveSecrets() = nil error, want error")
	}
	for _, want := range []string{"A", "MISSING_A", "B", "/no/such/file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "C ") {
		t.Errorf("error = %q, want it not to list what was read successfully", err.Error())
	}
}

// An optional secret that cannot be read is not a failure.
func TestResolveSecretsOptional(t *testing.T) {
	cfg := mustParse(t, planBase+`
secrets:
  MAYBE:
    env: NOT_SET
    optional: true
`)
	got, err := resolveSecrets(cfg, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("resolveSecrets() error = %v", err)
	}
	if _, ok := got["MAYBE"]; ok {
		t.Errorf("resolveSecrets() = %v, want it left out when it cannot be read", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine the home directory")
	}

	tests := map[string]string{
		"~/.config/key": filepath.Join(home, ".config/key"),
		"~":             home,
		"/abs/path":     "/abs/path",
		"relative":      "relative",
	}
	for in, want := range tests {
		got, err := expandHome(in)
		if err != nil {
			t.Fatalf("expandHome(%q) error = %v", in, err)
		}
		if got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadSecretErrors(t *testing.T) {
	t.Run("a path whose home cannot be resolved", func(t *testing.T) {
		t.Setenv("HOME", "")

		// Some environments resolve it even with HOME empty, so only assert when
		// it actually fails.
		if _, err := expandHome("~/x"); err != nil {
			cfg := mustParse(t, planBase+"secrets:\n  A:\n    file: ~/x\n")
			if _, err := resolveSecrets(cfg, os.LookupEnv); err == nil {
				t.Error("resolveSecrets() = nil error, want error")
			}
		}
	})
}

func TestStepKindGalaxy(t *testing.T) {
	cfg := mustParse(t, planBase+"provision:\n  - galaxy:\n      requirements: r.yml\n")

	if got := stepKind(cfg.Provision[0]); got != "galaxy" {
		t.Errorf("stepKind() = %q, want galaxy", got)
	}
}

func TestPlanActionsShowsGalaxyRequirements(t *testing.T) {
	cfg := mustParse(t, planBase+"provision:\n  - galaxy:\n      requirements: .incus-dev/requirements.yml\n")

	got := strings.Join(planActions(cfg, "dev-x", nil, idmapPlan{}), "\n")
	if !strings.Contains(got, "(galaxy .incus-dev/requirements.yml)") {
		t.Errorf("plan =\n%s", got)
	}
}

func TestInstanceNameForRequiresBranchFunc(t *testing.T) {
	cfg := mustParse(t, planBase)
	cfg.Project.Scope = "branch"

	if _, err := instanceNameFor(cfg, nil); err == nil {
		t.Error("error = nil, want a failure without a branchFunc")
	}
}

// A secret file named relatively is found from anywhere in the project.
//
// Discovery walks upwards, so running idev from a subdirectory is the normal
// case; resolving against the working directory made it work from the root and
// fail one level down (spec 03-configuration.md 3.11).
func TestSecretFileIsRelativeToTheProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".incus-dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".incus-dev", "token"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	cfg := &config.Config{
		Root:    root,
		Secrets: map[string]config.Secret{"TOK": {File: ".incus-dev/token"}},
	}

	got, err := resolveSecrets(cfg, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("resolveSecrets() error = %v", err)
	}
	if got["TOK"] != "s3cret" {
		t.Errorf("TOK = %q, want s3cret", got["TOK"])
	}
}

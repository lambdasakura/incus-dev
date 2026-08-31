package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

// 取得できないものは、どれが足りないかをまとめて報告する
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
			t.Errorf("error = %q, %q を含むこと", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "C ") {
		t.Errorf("error = %q, 取得できたものは挙げないこと", err.Error())
	}
}

// optional なものは取得できなくても失敗しない
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
		t.Errorf("resolveSecrets() = %v, 取得できなければ含めないこと", got)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("ホームディレクトリを取得できません")
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
	t.Run("ホームディレクトリを解決できないパス", func(t *testing.T) {
		t.Setenv("HOME", "")

		// 環境によっては HOME が空でも解決できるため、失敗する場合のみ検証する
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
		t.Error("error = nil, branchFuncが無ければ失敗すること")
	}
}

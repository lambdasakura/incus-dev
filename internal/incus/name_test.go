package incus_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/incus"
)

func TestInstanceName(t *testing.T) {
	tests := []struct {
		project string
		want    string
	}{
		{"example-project", "dev-example-project"},
		{"My.Project_1", "dev-my-project-1"},
		{"a", "dev-a"},
		{"UPPER", "dev-upper"},
		{"dots...everywhere", "dev-dots-everywhere"},
		{"trailing---", "dev-trailing"},
	}
	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			if got := incus.InstanceName(tt.project); got != tt.want {
				t.Errorf("InstanceName(%q) = %q, want %q", tt.project, got, tt.want)
			}
		})
	}
}

// Incusのinstance名は63文字までなので切り詰める（仕様 05-incus.md 5.1）
func TestInstanceNameLength(t *testing.T) {
	got := incus.InstanceName(strings.Repeat("abcdefghij", 10))

	if len(got) > 63 {
		t.Errorf("len(%q) = %d, want <= 63", got, len(got))
	}
	if !strings.HasPrefix(got, "dev-") {
		t.Errorf("InstanceName() = %q, dev- で始まること", got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("InstanceName() = %q, - で終わらないこと", got)
	}
}

// 生成される名前は常にIncusのinstance名制約を満たすこと
func TestInstanceNameDistinguishesNonAlphanumericNames(t *testing.T) {
	// 英数字を含まない名前が同じinstance名に潰れないこと
	a := incus.InstanceName("..")
	b := incus.InstanceName("---")

	if a == b {
		t.Errorf("InstanceName(..) と InstanceName(---) がどちらも %q になっている", a)
	}
	for _, got := range []string{a, b} {
		if !strings.HasPrefix(got, incus.InstanceNamePrefix) {
			t.Errorf("InstanceName() = %q", got)
		}
	}
}

func TestInstanceNameAlwaysValid(t *testing.T) {
	inputs := []string{
		"a", "UPPER", "my.project_1", "..", "---", "日本語プロジェクト",
		"with space", "sym!@#$%^&*()", "9leading-digit",
		strings.Repeat("x", 200), "trailing-", "-leading",
	}
	valid := regexp.MustCompile(`^[a-z0-9-]+$`)

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			got := incus.InstanceName(in)

			if !valid.MatchString(got) {
				t.Errorf("InstanceName(%q) = %q, 英数字とハイフンのみであること", in, got)
			}
			if len(got) > 63 {
				t.Errorf("InstanceName(%q) の長さ = %d, want <= 63", in, len(got))
			}
			if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
				t.Errorf("InstanceName(%q) = %q, ハイフンで始まらず終わらないこと", in, got)
			}
			if !strings.HasPrefix(got, incus.InstanceNamePrefix) {
				t.Errorf("InstanceName(%q) = %q, %q で始まること", in, got, incus.InstanceNamePrefix)
			}
		})
	}
}

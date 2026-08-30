package incus_test

import (
	"strings"
	"testing"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
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

func TestInstanceNameIsDeterministic(t *testing.T) {
	a := incus.InstanceName("some.project")
	b := incus.InstanceName("some.project")
	if a != b {
		t.Errorf("InstanceName() = %q, %q, 決定的であること", a, b)
	}
}

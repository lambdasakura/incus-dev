package incus_test

import (
	"regexp"
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

// Incus instance names top out at 63 characters, so they are truncated
// (spec 05-incus.md 5.1).
func TestInstanceNameLength(t *testing.T) {
	got := incus.InstanceName(strings.Repeat("abcdefghij", 10))

	if len(got) > 63 {
		t.Errorf("len(%q) = %d, want <= 63", got, len(got))
	}
	if !strings.HasPrefix(got, "dev-") {
		t.Errorf("InstanceName() = %q, want it to start with dev-", got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("InstanceName() = %q, want it not to end with -", got)
	}
}

// The generated name always satisfies what Incus accepts.
func TestInstanceNameDistinguishesNonAlphanumericNames(t *testing.T) {
	// Names with no letters or digits must not collapse into one instance name.
	a := incus.InstanceName("..")
	b := incus.InstanceName("---")

	if a == b {
		t.Errorf("InstanceName(..) and InstanceName(---) both gave %q", a)
	}
	for _, got := range []string{a, b} {
		if !strings.HasPrefix(got, incus.InstanceNamePrefix) {
			t.Errorf("InstanceName() = %q", got)
		}
	}
}

func TestInstanceNameAlwaysValid(t *testing.T) {
	inputs := []string{
		"a", "UPPER", "my.project_1", "..", "---", "日本語プロジェクト", // a non-ASCII name
		"with space", "sym!@#$%^&*()", "9leading-digit",
		strings.Repeat("x", 200), "trailing-", "-leading",
	}
	valid := regexp.MustCompile(`^[a-z0-9-]+$`)

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			got := incus.InstanceName(in)

			if !valid.MatchString(got) {
				t.Errorf("InstanceName(%q) = %q, want only letters, digits and hyphens", in, got)
			}
			if len(got) > 63 {
				t.Errorf("len(InstanceName(%q)) = %d, want <= 63", in, len(got))
			}
			if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
				t.Errorf("InstanceName(%q) = %q, want no leading or trailing hyphen", in, got)
			}
			if !strings.HasPrefix(got, incus.InstanceNamePrefix) {
				t.Errorf("InstanceName(%q) = %q, want it to start with %q", in, got, incus.InstanceNamePrefix)
			}
		})
	}
}

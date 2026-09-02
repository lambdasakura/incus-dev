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

// A long project name must not swallow the suffix that tells checkouts apart.
//
// project.scope exists so that several checkouts of one project get their own
// instance (spec 05-incus.md 5.1). Truncating the whole name to what Incus
// accepts used to cut the suffix off the end, collapsing them into one.
func TestInstanceNameWithSuffixKeepsTheSuffix(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10)

	a := incus.InstanceNameWithSuffix(long, incus.ShortHash("/home/u/a"))
	b := incus.InstanceNameWithSuffix(long, incus.ShortHash("/home/u/b"))

	if a == b {
		t.Fatalf("both checkouts became %q, want one instance each", a)
	}
	for _, got := range []string{a, b} {
		if len(got) > 63 {
			t.Errorf("len(%q) = %d, want <= 63", got, len(got))
		}
		if !strings.HasPrefix(got, "dev-") {
			t.Errorf("InstanceNameWithSuffix() = %q, want it to start with dev-", got)
		}
	}
	if !strings.HasSuffix(a, incus.ShortHash("/home/u/a")) {
		t.Errorf("InstanceNameWithSuffix() = %q, want it to end with the suffix", a)
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

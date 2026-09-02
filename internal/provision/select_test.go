package provision_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/lambdasakura/incus-dev/internal/provision"
)

func selectSteps(t *testing.T, yaml string, sel provision.Selection) []int {
	t.Helper()

	cfg := parseConfig(t, yaml)
	got, err := provision.Select(cfg.Provision, sel)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	return got
}

const threeSteps = base + `
provision:
  - name: base packages
    run: "true"
  - name: provision
    ansible:
      playbook: p.yml
  - run: "true"
`

func TestSelectAllByDefault(t *testing.T) {
	got := selectSteps(t, threeSteps, provision.Selection{})

	if diff := cmp.Diff([]int{0, 1, 2}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectOnly(t *testing.T) {
	tests := []struct {
		name string
		only []string
		want []int
	}{
		{"by name", []string{"provision"}, []int{1}},
		{"by number", []string{"2"}, []int{1}},
		{"several at once", []string{"base packages", "3"}, []int{0, 2}},
		{"an unnamed step by number", []string{"step 3"}, []int{2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectSteps(t, threeSteps, provision.Selection{Only: tt.only})

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Select() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSelectFrom(t *testing.T) {
	tests := []struct {
		name string
		from string
		want []int
	}{
		{"by name", "provision", []int{1, 2}},
		{"by number", "1", []int{0, 1, 2}},
		{"the last step", "3", []int{2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectSteps(t, threeSteps, provision.Selection{From: tt.from})

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Select() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Several steps sharing a name are all selected.
func TestSelectMatchesAllWithSameName(t *testing.T) {
	got := selectSteps(t, base+`
provision:
  - name: setup
    run: "true"
  - name: other
    run: "true"
  - name: setup
    run: "true"
`, provision.Selection{Only: []string{"setup"}})

	if diff := cmp.Diff([]int{0, 2}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

func TestSelectErrors(t *testing.T) {
	tests := []struct {
		name string
		sel  provision.Selection
		want []string // strings the error must contain
	}{
		{
			name: "a name that does not exist",
			sel:  provision.Selection{Only: []string{"nope"}},
			want: []string{"nope", "base packages", "provision"},
		},
		{
			name: "a number out of range",
			sel:  provision.Selection{Only: []string{"9"}},
			want: []string{"9"},
		},
		{
			name: "from does not exist",
			sel:  provision.Selection{From: "nope"},
			want: []string{"nope"},
		},
		{
			name: "only and from together",
			sel:  provision.Selection{Only: []string{"provision"}, From: "provision"},
			want: []string{"only", "from"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := parseConfig(t, threeSteps)

			_, err := provision.Select(cfg.Provision, tt.sel)
			if err == nil {
				t.Fatal("Select() = nil error, want error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), want)
				}
			}
		})
	}
}

// Naming a step when there are none is an error.
func TestSelectWithoutSteps(t *testing.T) {
	cfg := parseConfig(t, base)

	if _, err := provision.Select(cfg.Provision, provision.Selection{Only: []string{"x"}}); err == nil {
		t.Error("Select() = nil error, want error")
	}
	if got, err := provision.Select(cfg.Provision, provision.Selection{}); err != nil || len(got) != 0 {
		t.Errorf("Select() = %v, %v, want empty when nothing was asked for", got, err)
	}
}

// Naming the same step twice still runs it once.
func TestSelectDeduplicates(t *testing.T) {
	got := selectSteps(t, threeSteps, provision.Selection{Only: []string{"provision", "2"}})

	if diff := cmp.Diff([]int{1}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

// A step's own name wins over reading the reference as a position.
//
// Otherwise a step named "2" cannot be selected at all, and asking for it
// silently runs whatever sits at position 2 instead.
func TestSelectPrefersAStepNamedLikeANumber(t *testing.T) {
	yaml := base + `
provision:
  - name: setup
    run: "true"
  - name: build
    run: "true"
  - name: "2"
    run: "true"
`

	got := selectSteps(t, yaml, provision.Selection{Only: []string{"2"}})

	if diff := cmp.Diff([]int{2}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

// A number that names no step is still a position.
func TestSelectFallsBackToThePosition(t *testing.T) {
	got := selectSteps(t, threeSteps, provision.Selection{Only: []string{"2"}})

	if diff := cmp.Diff([]int{1}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

// A step's own name and the placeholder for an unnamed one are different
// things, so one must not match the other.
//
// DisplayName returns "step N" for an unnamed step, and matching against it
// put both in one namespace: a step named "step 2" then also matched the
// unnamed step at position 2.
func TestSelectDoesNotMatchThePlaceholderOfAnotherStep(t *testing.T) {
	yaml := base + `
provision:
  - name: "step 2"
    run: "true"
  - run: "true"
  - name: third
    run: "true"
`

	t.Run("only", func(t *testing.T) {
		got := selectSteps(t, yaml, provision.Selection{Only: []string{"step 2"}})
		if diff := cmp.Diff([]int{0}, got); diff != "" {
			t.Errorf("Select() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("from", func(t *testing.T) {
		got := selectSteps(t, yaml, provision.Selection{From: "step 2"})
		if diff := cmp.Diff([]int{0, 1, 2}, got); diff != "" {
			t.Errorf("Select() mismatch (-want +got):\n%s", diff)
		}
	})
}

// The placeholder still names an unnamed step.
func TestSelectMatchesThePlaceholderOfAnUnnamedStep(t *testing.T) {
	yaml := base + `
provision:
  - name: setup
    run: "true"
  - run: "true"
`

	got := selectSteps(t, yaml, provision.Selection{Only: []string{"step 2"}})

	if diff := cmp.Diff([]int{1}, got); diff != "" {
		t.Errorf("Select() mismatch (-want +got):\n%s", diff)
	}
}

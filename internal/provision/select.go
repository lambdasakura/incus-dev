package provision

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
)

// Selection narrows down which steps to run.
//
// Re-running everything gets expensive as the number of steps grows, so while
// iterating you can run only part of it (spec 04-cli.md 4.2).
type Selection struct {
	// Only names the steps to run, by name or by 1-based number.
	Only []string
	// From names the step to start at; everything after it runs too.
	From string
}

// IsZero reports whether no narrowing was asked for.
func (s Selection) IsZero() bool {
	return len(s.Only) == 0 && s.From == ""
}

// Select returns the positions of the steps to run, or all of them when
// nothing was asked for.
func Select(steps []config.Step, sel Selection) ([]int, error) {
	if len(sel.Only) > 0 && sel.From != "" {
		return nil, fmt.Errorf("only and from cannot be used together")
	}

	all := make([]int, len(steps))
	for i := range steps {
		all[i] = i
	}
	if sel.IsZero() {
		return all, nil
	}

	if sel.From != "" {
		matched, err := match(steps, sel.From)
		if err != nil {
			return nil, err
		}
		return all[matched[0]:], nil
	}

	var out []int
	for _, ref := range sel.Only {
		matched, err := match(steps, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, matched...)
	}

	// Run in declaration order, not in the order given: steps are written on
	// the assumption that they build on the ones before them.
	out = dedup(out)
	slices.Sort(out)

	return out, nil
}

// match returns the positions of the steps matching a name or a number.
func match(steps []config.Step, ref string) ([]int, error) {
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(steps) {
			return nil, fmt.Errorf("step %d is out of range (1-%d)%s", n, len(steps), available(steps))
		}
		return []int{n - 1}, nil
	}

	var out []int
	for i, s := range steps {
		if s.DisplayName(i+1) == ref {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no step named %q%s", ref, available(steps))
	}
	return out, nil
}

// available lists the steps that can be chosen.
func available(steps []config.Step) string {
	if len(steps) == 0 {
		return "; this project declares no provision steps"
	}

	names := make([]string, 0, len(steps))
	for i, s := range steps {
		names = append(names, fmt.Sprintf("%d. %s", i+1, s.DisplayName(i+1)))
	}
	return "\navailable steps:\n  " + strings.Join(names, "\n  ")
}

func dedup(indices []int) []int {
	seen := make(map[int]bool, len(indices))
	out := make([]int, 0, len(indices))

	for _, i := range indices {
		if seen[i] {
			continue
		}
		seen[i] = true
		out = append(out, i)
	}
	return out
}

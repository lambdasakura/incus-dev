package incustest_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lambdasakura/incus-dev/internal/incus/contract"
	"github.com/lambdasakura/incus-dev/internal/incus/incustest"
)

// The fake has to behave like Incus for the operations idev uses.
//
// It is what almost every unit test in this repository runs against, so a
// place where it differs is a place where a wrong belief about Incus passes
// the whole suite. That is how every regression in fifteen rounds of review
// got through (spec 08-testing.md 8.3.1).
//
// The same assertions run against the real daemon in
// test/integration/contract_test.go. Any divergence fails one of the two.
func TestFakeMeetsTheClientContract(t *testing.T) {
	f := incustest.New()
	f.Profiles = []string{"default"}
	f.Pools = []string{"default"}
	// Only this one resolves, so CheckImage has something to refuse.
	f.Images = []string{"images:alpine/3.21"}

	ran := contract.Run(t, contract.Env{
		Client:      f,
		Instance:    "dev-contract",
		Image:       "images:alpine/3.21",
		Pool:        "default",
		MissingPool: "no-such-pool",
		Profile:     "default",
	})

	// Gutting Run would otherwise pass here and in the integration suite
	// alike, which is the trap the contract exists to close.
	if err := contract.Verify(ran); err != nil {
		t.Error(err)
	}

	// An independent witness: what the fake was actually asked to do. Run
	// reports on itself, so a rewrite of it could report a full contract
	// while doing nothing; the call log cannot be produced without the
	// checks running.
	for _, want := range []string{
		"create dev-contract", "start dev-contract", "delete dev-contract",
		"volume create default", "volume delete default",
		"snapshot create dev-contract", "exec dev-contract",
		"config dev-contract", "devices dev-contract",
	} {
		if !slices.ContainsFunc(f.Calls, func(c string) bool {
			return strings.HasPrefix(c, want)
		}) {
			t.Errorf("the fake was never asked to %q; the contract did not run", want)
		}
	}
}

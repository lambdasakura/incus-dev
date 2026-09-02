//go:build integration

package integration_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/incus/contract"
)

// The same contract the fake is held to, against the real daemon.
//
// A fake nobody checks is a belief nobody checks. Both runs assert the same
// things, so a divergence fails one of them rather than sitting in the fake
// waiting to pass a wrong unit test (spec 08-testing.md 8.3.1).
func TestIncusMeetsTheClientContract(t *testing.T) {
	requireIncus(t)

	client, err := incus.Connect(incus.Target{Project: "default"})
	if err != nil {
		t.Fatalf("connect to Incus: %v", err)
	}

	name := fmt.Sprintf("dev-contract-%d", time.Now().UnixNano()%1e9)
	t.Cleanup(func() { _, _ = runIncus("delete", "--force", name) })

	ran := contract.Run(t, contract.Env{
		Client:       client,
		Instance:     name,
		Image:        testImage,
		Pool:         "default",
		MissingPool:  "idev-contract-no-such-pool",
		Profile:      "default",
		RunsPrograms: true,
	})

	// Gutting Run would otherwise pass here and in the integration suite
	// alike, which is the trap the contract exists to close.
	if err := contract.Verify(ran); err != nil {
		t.Error(err)
	}
}

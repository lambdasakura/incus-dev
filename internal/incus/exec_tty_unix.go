//go:build !windows

package incus

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize subscribes to window-size changes of the host terminal. It
// returns the channel they arrive on and a function ending the subscription.
func notifyResize() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)

	return signals, func() { signal.Stop(signals) }
}

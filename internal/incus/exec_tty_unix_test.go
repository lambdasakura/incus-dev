//go:build !windows

package incus

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// SIGWINCH is turned into a window-size notification.
func TestOSConsoleResized(t *testing.T) {
	console := &osConsole{In: os.Stdin, Out: os.Stdout}

	resized, stop := console.Resized()

	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resized:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGWINCH was never notified")
	}

	stop()

	// Ending the subscription closes the channel and ends the sending loop.
	select {
	case _, ok := <-resized:
		if ok {
			t.Error("a notification arrived after stopping")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel never closed after stopping")
	}
}

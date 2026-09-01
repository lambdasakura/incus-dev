//go:build windows

package incus

import (
	"os"
	"testing"
	"time"
)

// Windows reports no window-size changes, but the subscription must still
// behave: it stays quiet and closes when stopped.
func TestOSConsoleResized(t *testing.T) {
	console := &osConsole{In: os.Stdin, Out: os.Stdout}

	resized, stop := console.Resized()

	select {
	case <-resized:
		t.Fatal("a window-size change was reported on a platform that has none")
	case <-time.After(100 * time.Millisecond):
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

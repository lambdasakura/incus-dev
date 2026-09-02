//go:build windows

package incus

import "os"

// notifyResize has nothing to subscribe to. Windows reports window-size
// changes as console events rather than as a signal, and there is no
// SIGWINCH. The nil channel it returns never becomes ready, so the size sent
// when the exec starts is the only one the container is told about.
func notifyResize() (<-chan os.Signal, func()) {
	return nil, func() {}
}

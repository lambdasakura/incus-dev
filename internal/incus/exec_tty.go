package incus

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/lxc/incus/v6/shared/api"
	"golang.org/x/term"
)

// Console is the host terminal, used when running with a TTY.
type Console interface {
	// Size returns the terminal's width and height.
	Size() (width, height int, err error)
	// MakeRaw puts the terminal into raw mode and returns a function that
	// restores it.
	MakeRaw() (restore func(), err error)
	// Resized returns a channel notified on window-size changes, and a
	// function that ends the subscription. Ending it closes the channel.
	// Call stop exactly once.
	Resized() (resized <-chan struct{}, stop func())
}

// osConsole is a Console bound to the process's own standard streams.
type osConsole struct {
	In  *os.File
	Out *os.File
}

func (c *osConsole) Size() (int, int, error) {
	return term.GetSize(int(c.Out.Fd()))
}

func (c *osConsole) MakeRaw() (func(), error) {
	fd := int(c.In.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("switch terminal to raw mode: %w", err)
	}
	return func() { _ = term.Restore(fd, state) }, nil
}

func (c *osConsole) Resized() (<-chan struct{}, func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)

	resized := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer close(resized)
		for {
			select {
			case <-signals:
				select {
				case resized <- struct{}{}:
				default:
					// A notification already pending will read the latest size.
				}
			case <-done:
				return
			}
		}
	}()

	return resized, func() {
		signal.Stop(signals)
		close(done)
	}
}

// control is what the control websocket carries.
type control struct {
	// ctx ending tells the process in the container to stop.
	ctx context.Context
	// done marks the exec as finished, ending the handler.
	done <-chan struct{}
	// console is the terminal, nil when running without one.
	console Console
	// resized notifies of window-size changes, nil when running without a
	// terminal.
	resized <-chan struct{}
	// sent is closed once an interruption has been forwarded to the container,
	// so the caller can wait for it rather than exiting first.
	sent chan struct{}
}

// forwardInterrupt tells the process in the container to stop.
//
// Left alone, a package installation keeps running and collides with the next
// run (spec 05-incus.md 5.7.3).
func (c control) forwardInterrupt(send func(any) error) {
	_ = send(signalMessage(syscall.SIGTERM))
	c.finish()
}

// finish releases whoever is waiting for the signal to go out.
//
// Every exit from handle goes through it: retiring on a failed send without
// closing sent leaves the caller waiting out the whole grace period for a
// signal nothing will ever send.
func (c control) finish() {
	if c.sent == nil {
		return
	}
	select {
	case <-c.sent:
		// Already closed by forwardInterrupt.
	default:
		close(c.sent)
	}
}

// controlHandler returns the handler for the control websocket.
//
// Incus knows neither the terminal size nor the signals on the host side, so
// this is the channel that tells it.
func controlHandler(c control) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		defer func() {
			msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = conn.WriteMessage(websocket.CloseMessage, msg)
		}()

		c.handle(conn.WriteJSON)
	}
}

// handle keeps sending control messages.
//
// When sending stops working it exits quietly. The cost is a garbled display
// or a dropped signal, whereas treating a failure here as a failure of the
// run itself would destroy the user's work.
func (c control) handle(send func(any) error) {
	defer c.finish()

	for {
		select {
		case <-c.done:
			// Exec closes done as it returns, so an interruption makes both
			// this and ctx.Done() ready at once and select picks at random.
			// The interruption has to win either way.
			if c.ctx.Err() != nil {
				c.forwardInterrupt(send)
			}
			return

		case <-c.ctx.Done():
			c.forwardInterrupt(send)
			return

		case _, ok := <-c.resized:
			if !ok {
				// The subscription ended. Stop waiting on resizes.
				c.resized = nil
				continue
			}
			width, height, err := c.console.Size()
			if err != nil {
				// A local failure to read the size says nothing about the
				// control websocket. Returning here would give up forwarding
				// an interruption for the rest of the run.
				continue
			}
			if err := send(resizeMessage(width, height)); err != nil {
				return
			}
		}
	}
}

func resizeMessage(width, height int) api.InstanceExecControl {
	return api.InstanceExecControl{
		Command: "window-resize",
		Args: map[string]string{
			"width":  strconv.Itoa(width),
			"height": strconv.Itoa(height),
		},
	}
}

func signalMessage(sig syscall.Signal) api.InstanceExecControl {
	return api.InstanceExecControl{Command: "signal", Signal: int(sig)}
}

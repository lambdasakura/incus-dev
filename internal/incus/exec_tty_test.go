package incus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	"github.com/lxc/incus/v6/shared/api"
)

// fakeConsole is a fake of the host terminal.
type fakeConsole struct {
	width, height int
	sizeErr       error
	rawErr        error

	raw      bool
	restored bool
	stopped  bool
	resized  chan struct{}
}

func newFakeConsole() *fakeConsole {
	return &fakeConsole{width: 80, height: 24, resized: make(chan struct{}, 1)}
}

func (c *fakeConsole) Size() (int, int, error) {
	if c.sizeErr != nil {
		return 0, 0, c.sizeErr
	}
	return c.width, c.height, nil
}

func (c *fakeConsole) MakeRaw() (func(), error) {
	if c.rawErr != nil {
		return nil, c.rawErr
	}
	c.raw = true
	return func() { c.restored = true }, nil
}

func (c *fakeConsole) Resized() (<-chan struct{}, func()) {
	return c.resized, func() { c.stopped = true }
}

// Running with a terminal switches to raw mode and asks for an interactive exec.
func TestAPIExecInteractive(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	console := newFakeConsole()
	a, _ := newAPI(f)
	a.Console = console

	code, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d", code)
	}

	req := f.lastExec
	if !req.Interactive {
		t.Error("Interactive = false, want it requested as a run with a terminal")
	}
	if req.Width != 80 || req.Height != 24 {
		t.Errorf("size = %dx%d, want 80x24", req.Width, req.Height)
	}
	if !console.raw {
		t.Error("never switched to raw mode")
	}
	if !console.restored {
		t.Error("never restored the terminal")
	}
	if !console.stopped {
		t.Error("never ended the window-size subscription")
	}
	if f.lastExecArgs.Control == nil {
		t.Error("no handler was set to send window-size changes")
	}
}

// Leaving without restoring the terminal breaks the user's shell, so it is
// restored even on failure.
func TestAPIExecInteractiveRestoresOnError(t *testing.T) {
	f := newFakeServer()
	f.err["ExecInstance"] = errAPI
	console := newFakeConsole()
	a, _ := newAPI(f)
	a.Console = console

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true}); !errors.Is(err, errAPI) {
		t.Fatalf("error = %v, want %v", err, errAPI)
	}
	if !console.restored {
		t.Error("want the terminal restored on failure too")
	}
}

// It does not run when raw mode cannot be entered.
func TestAPIExecInteractiveRawModeError(t *testing.T) {
	f := newFakeServer()
	console := newFakeConsole()
	console.rawErr = errAPI
	a, _ := newAPI(f)
	a.Console = console

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true}); !errors.Is(err, errAPI) {
		t.Fatalf("error = %v, want %v", err, errAPI)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v, want nothing run without a usable terminal", f.calls)
	}
}

// When the terminal size is unknown, Incus's default is left to decide.
func TestAPIExecInteractiveWithoutSize(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	console := newFakeConsole()
	console.sizeErr = errAPI
	a, _ := newAPI(f)
	a.Console = console

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{TTY: true}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if f.lastExec.Width != 0 || f.lastExec.Height != 0 {
		t.Errorf("size = %dx%d, want nothing sent when it cannot be read", f.lastExec.Width, f.lastExec.Height)
	}
}

// A run without a terminal does not touch the terminal.
func TestAPIExecDoesNotTouchConsole(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	console := newFakeConsole()
	a, _ := newAPI(f)
	a.Console = console

	if _, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if console.raw {
		t.Error("entered raw mode for a run without a terminal")
	}
	// The control channel opens even without a terminal, so an interruption can
	// get through.
	if f.lastExecArgs.Control == nil {
		t.Error("no control handler was set")
	}
}

// Window-size changes reach Incus.
func TestControlSendsResize(t *testing.T) {
	console := newFakeConsole()
	resized := make(chan struct{}, 1)
	resized <- struct{}{}
	done := make(chan struct{})

	var sent []any
	send := func(v any) error {
		sent = append(sent, v)
		console.width, console.height = 100, 40
		close(done) // the next iteration exits
		return nil
	}

	control{ctx: context.Background(), done: done, console: console, resized: resized}.handle(send)

	want := []any{api.InstanceExecControl{
		Command: "window-resize",
		Args:    map[string]string{"width": "80", "height": "24"},
	}}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("sent mismatch (-want +got):\n%s", diff)
	}
}

// An interruption tells the process in the container to stop. Left alone, a
// package installation keeps running and collides with the next run.
func TestControlForwardsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var sent []any
	control{ctx: ctx, done: make(chan struct{})}.handle(func(v any) error {
		sent = append(sent, v)
		return nil
	})

	want := []any{api.InstanceExecControl{Command: "signal", Signal: int(syscall.SIGTERM)}}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("sent mismatch (-want +got):\n%s", diff)
	}
}

// The handler ends when the run does.
func TestControlStopsWhenExecFinishes(t *testing.T) {
	done := make(chan struct{})
	close(done)

	control{ctx: context.Background(), done: done}.handle(func(any) error {
		t.Error("sent something after the run finished")
		return nil
	})
}

// The handler keeps watching for an interruption after the subscription ends.
func TestControlSurvivesClosedResize(t *testing.T) {
	resized := make(chan struct{})
	close(resized)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	control{ctx: ctx, done: make(chan struct{}), console: newFakeConsole(), resized: resized}.handle(
		func(any) error {
			calls++
			return nil
		})

	if calls != 1 {
		t.Errorf("sends = %d, want the interruption alone", calls)
	}
}

// It exits quietly once sending stops working; the exec itself carries on.
func TestControlStopsOnSendError(t *testing.T) {
	console := newFakeConsole()
	resized := make(chan struct{}, 2)
	resized <- struct{}{}
	resized <- struct{}{}

	calls := 0
	control{ctx: context.Background(), done: make(chan struct{}), console: console, resized: resized}.handle(
		func(any) error {
			calls++
			return errAPI
		})

	if calls != 1 {
		t.Errorf("sends = %d, want no retry after a failure", calls)
	}
}

// Nothing is sent when the size cannot be read.
func TestControlWithoutSize(t *testing.T) {
	console := newFakeConsole()
	console.sizeErr = errAPI
	resized := make(chan struct{}, 1)
	resized <- struct{}{}

	control{ctx: context.Background(), done: make(chan struct{}), console: console, resized: resized}.handle(
		func(any) error {
			t.Error("sent something despite not knowing the size")
			return nil
		})
}

// Raw mode is not possible on streams that are not a terminal.
func TestOSConsoleRequiresTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	console := &osConsole{In: r, Out: w}

	if _, err := console.MakeRaw(); err == nil {
		t.Error("a pipe must not be switchable to raw mode")
	}
	if _, _, err := console.Size(); err == nil {
		t.Error("a pipe has no terminal size to read")
	}
}

// It sends the size over the control websocket, and a close at the end.
func TestControlHandler(t *testing.T) {
	type received struct {
		control api.InstanceExecControl
		closed  bool
	}
	got := make(chan received, 2)

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade error = %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			var msg api.InstanceExecControl
			if err := conn.ReadJSON(&msg); err != nil {
				got <- received{closed: websocket.IsCloseError(err, websocket.CloseNormalClosure)}
				return
			}
			got <- received{control: msg}
		}
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	console := newFakeConsole()
	resized := make(chan struct{}, 1)
	resized <- struct{}{}

	// Once one arrives, treat the exec as finished so the handler exits.
	done := make(chan struct{})
	first := make(chan received, 1)
	go func() {
		first <- <-got
		close(done)
	}()

	controlHandler(control{
		ctx: context.Background(), done: done, console: console, resized: resized,
	})(conn)

	want := api.InstanceExecControl{
		Command: "window-resize",
		Args:    map[string]string{"width": "80", "height": "24"},
	}
	if diff := cmp.Diff(want, (<-first).control); diff != "" {
		t.Errorf("sent mismatch (-want +got):\n%s", diff)
	}
	if last := <-got; !last.closed {
		t.Error("no close was sent at the end")
	}
}

// With a terminal allocated, vim and less need TERM to be passed.
func TestAPIExecInteractivePassesTerm(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)
	a.Console = newFakeConsole()

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{
		TTY:  true,
		Term: "xterm-256color",
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := f.lastExec.Environment["TERM"]; got != "xterm-256color" {
		t.Errorf("TERM = %q, want xterm-256color", got)
	}
}

// A run without a terminal does not get TERM, so nothing is tempted into
// terminal-oriented output such as a progress bar.
func TestAPIExecWithoutTTYOmitsTerm(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)

	if _, err := a.Exec(context.Background(), "dev-x", []string{"true"}, ExecOptions{
		Term: "xterm-256color",
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if _, ok := f.lastExec.Environment["TERM"]; ok {
		t.Errorf("environment = %v, want TERM not passed", f.lastExec.Environment)
	}
}

// A TERM the project set wins.
func TestAPIExecTermCanBeOverridden(t *testing.T) {
	f := newFakeServer()
	f.addInstance("dev-x", api.InstancePut{})
	a, _ := newAPI(f)
	a.Console = newFakeConsole()

	if _, err := a.Exec(context.Background(), "dev-x", []string{"/bin/sh"}, ExecOptions{
		TTY:  true,
		Term: "xterm-256color",
		Env:  map[string]string{"TERM": "dumb"},
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := f.lastExec.Environment["TERM"]; got != "dumb" {
		t.Errorf("TERM = %q, want the explicit setting to win", got)
	}
}

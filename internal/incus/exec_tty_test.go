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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	"github.com/lxc/incus/v6/shared/api"
)

// fakeConsole はホスト端末のfake。
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

// 端末を伴う実行では、raw modeへ切り替えたうえでinteractiveなexecを要求する
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
		t.Error("Interactive = false, 端末を伴う実行として要求すること")
	}
	if req.Width != 80 || req.Height != 24 {
		t.Errorf("size = %dx%d, want 80x24", req.Width, req.Height)
	}
	if !console.raw {
		t.Error("raw modeへ切り替えていない")
	}
	if !console.restored {
		t.Error("端末を復元していない")
	}
	if !console.stopped {
		t.Error("ウィンドウサイズ変更の購読を終えていない")
	}
	if f.lastExecArgs.Control == nil {
		t.Error("ウィンドウサイズ変更を送るハンドラが設定されていない")
	}
}

// 端末を復元しないまま抜けるとシェルが壊れるため、失敗しても必ず戻す
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
		t.Error("失敗時も端末を復元すること")
	}
}

// raw modeへ切り替えられない場合は実行しない
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
		t.Errorf("calls = %v, 端末を用意できないまま実行しないこと", f.calls)
	}
}

// 端末サイズが分からない場合はIncus側の既定に任せる
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
		t.Errorf("size = %dx%d, 取得できない場合は指定しないこと", f.lastExec.Width, f.lastExec.Height)
	}
}

// 端末を使わない実行では端末に触れない
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
		t.Error("端末を伴わない実行でraw modeにしている")
	}
	if f.lastExecArgs.Control != nil {
		t.Error("端末を伴わない実行で制御ハンドラを設定している")
	}
}

// ウィンドウサイズの変更をIncusへ伝える
func TestSendResizes(t *testing.T) {
	console := newFakeConsole()
	resized := make(chan struct{}, 2)
	resized <- struct{}{}

	var sent []any
	send := func(v any) error {
		sent = append(sent, v)
		console.width, console.height = 100, 40
		close(resized) // 2回目のループで抜ける
		return nil
	}

	sendResizes(console, resized, send)

	want := []any{api.InstanceExecControl{
		Command: "window-resize",
		Args:    map[string]string{"width": "80", "height": "24"},
	}}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("送信内容 mismatch (-want +got):\n%s", diff)
	}
}

// 送信できなくなったら黙って終える（execそのものは継続する）
func TestSendResizesStopsOnError(t *testing.T) {
	console := newFakeConsole()
	resized := make(chan struct{}, 2)
	resized <- struct{}{}
	resized <- struct{}{}

	calls := 0
	sendResizes(console, resized, func(any) error {
		calls++
		return errAPI
	})

	if calls != 1 {
		t.Errorf("送信回数 = %d, 失敗したら繰り返さないこと", calls)
	}
}

// サイズを取得できなければ送らない
func TestSendResizesWithoutSize(t *testing.T) {
	console := newFakeConsole()
	console.sizeErr = errAPI
	resized := make(chan struct{}, 1)
	resized <- struct{}{}

	sendResizes(console, resized, func(any) error {
		t.Error("サイズが分からないのに送信している")
		return nil
	})
}

// 端末でない入出力ではraw modeにできない
func TestOSConsoleRequiresTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()

	console := &osConsole{In: r, Out: w}

	if _, err := console.MakeRaw(); err == nil {
		t.Error("パイプをraw modeにできてはいけない")
	}
	if _, _, err := console.Size(); err == nil {
		t.Error("パイプの端末サイズは取得できない")
	}
}

// SIGWINCH を受け取ってウィンドウサイズ変更を通知する
func TestOSConsoleResized(t *testing.T) {
	console := &osConsole{In: os.Stdin, Out: os.Stdout}

	resized, stop := console.Resized()

	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	select {
	case <-resized:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGWINCH が通知されない")
	}

	stop()

	// 購読を終えるとチャネルが閉じ、送信側のループが終わること
	select {
	case _, ok := <-resized:
		if ok {
			t.Error("停止後に通知が届いている")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("停止してもチャネルが閉じない")
	}
}

// 制御用websocketへサイズを送り、終了時にcloseを送る
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
	close(resized)

	controlHandler(console, resized)(conn)

	first := <-got
	want := api.InstanceExecControl{
		Command: "window-resize",
		Args:    map[string]string{"width": "80", "height": "24"},
	}
	if diff := cmp.Diff(want, first.control); diff != "" {
		t.Errorf("送信内容 mismatch (-want +got):\n%s", diff)
	}
	if second := <-got; !second.closed {
		t.Error("終了時にcloseを送っていない")
	}
}

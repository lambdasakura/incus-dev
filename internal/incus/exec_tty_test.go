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
	// 中断を伝えるため、端末を伴わない実行でも制御経路は開く
	if f.lastExecArgs.Control == nil {
		t.Error("制御ハンドラが設定されていない")
	}
}

// ウィンドウサイズの変更をIncusへ伝える
func TestControlSendsResize(t *testing.T) {
	console := newFakeConsole()
	resized := make(chan struct{}, 1)
	resized <- struct{}{}
	done := make(chan struct{})

	var sent []any
	send := func(v any) error {
		sent = append(sent, v)
		console.width, console.height = 100, 40
		close(done) // 次のループで抜ける
		return nil
	}

	control{ctx: context.Background(), done: done, console: console, resized: resized}.handle(send)

	want := []any{api.InstanceExecControl{
		Command: "window-resize",
		Args:    map[string]string{"width": "80", "height": "24"},
	}}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("送信内容 mismatch (-want +got):\n%s", diff)
	}
}

// 中断されたら、コンテナ内のプロセスへ終了を伝える。
// 伝えないとパッケージ導入などが走り続け、次の実行と衝突する。
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
		t.Errorf("送信内容 mismatch (-want +got):\n%s", diff)
	}
}

// 実行が終わったらハンドラも終える
func TestControlStopsWhenExecFinishes(t *testing.T) {
	done := make(chan struct{})
	close(done)

	control{ctx: context.Background(), done: done}.handle(func(any) error {
		t.Error("実行後に送信している")
		return nil
	})
}

// 購読が終わってもハンドラは中断の監視を続ける
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
		t.Errorf("送信回数 = %d, 中断だけを送ること", calls)
	}
}

// 送信できなくなったら黙って終える（execそのものは継続する）
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
		t.Errorf("送信回数 = %d, 失敗したら繰り返さないこと", calls)
	}
}

// サイズを取得できなければ送らない
func TestControlWithoutSize(t *testing.T) {
	console := newFakeConsole()
	console.sizeErr = errAPI
	resized := make(chan struct{}, 1)
	resized <- struct{}{}

	control{ctx: context.Background(), done: make(chan struct{}), console: console, resized: resized}.handle(
		func(any) error {
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

	// 1件届いたらexecが終わった扱いにして、ハンドラを終えさせる
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
		t.Errorf("送信内容 mismatch (-want +got):\n%s", diff)
	}
	if last := <-got; !last.closed {
		t.Error("終了時にcloseを送っていない")
	}
}

// 端末を割り当てる場合、TERM を渡さないと vim / less などが動かない
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

// 端末を伴わない実行では TERM を渡さない
// （進捗バーなど、端末向けの出力を誘発しないため）
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
		t.Errorf("environment = %v, TERM を渡さないこと", f.lastExec.Environment)
	}
}

// プロジェクトが TERM を指定した場合はそちらを尊重する
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
		t.Errorf("TERM = %q, 明示指定を優先すること", got)
	}
}

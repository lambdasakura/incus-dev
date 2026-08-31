package incus

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gorilla/websocket"
	"github.com/lxc/incus/v6/shared/api"
	"golang.org/x/term"
)

// Console はホスト側の端末。TTYを伴う実行で使う。
type Console interface {
	// Size は端末の桁数・行数を返す。
	Size() (width, height int, err error)
	// MakeRaw は端末をraw modeへ切り替え、元へ戻す関数を返す。
	MakeRaw() (restore func(), err error)
	// Resized はウィンドウサイズ変更の通知チャネルと、購読を終える関数を返す。
	// 購読を終えるとチャネルは閉じる。
	Resized() (resized <-chan struct{}, stop func())
}

// osConsole はプロセスの標準入出力に結び付いた Console。
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
					// 未処理の通知が残っていれば、それが最新のサイズを読む。
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

// controlHandler は制御用websocketを扱うハンドラを返す。
//
// Incusは端末サイズを知らないため、変更をこの経路で伝える。
func controlHandler(console Console, resized <-chan struct{}) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		defer func() {
			msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = conn.WriteMessage(websocket.CloseMessage, msg)
		}()

		sendResizes(console, resized, conn.WriteJSON)
	}
}

// sendResizes は端末サイズの変更をIncusへ送り続ける。
//
// 送信できなくなった場合は黙って終える。表示の乱れにとどまる一方、
// ここでの失敗を実行そのものの失敗として扱うと利用者の作業を壊すため。
func sendResizes(console Console, resized <-chan struct{}, send func(any) error) {
	for range resized {
		width, height, err := console.Size()
		if err != nil {
			return
		}
		if err := send(resizeMessage(width, height)); err != nil {
			return
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

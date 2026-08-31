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

// Console はホスト側の端末。TTYを伴う実行で使う。
type Console interface {
	// Size は端末の桁数・行数を返す。
	Size() (width, height int, err error)
	// MakeRaw は端末をraw modeへ切り替え、元へ戻す関数を返す。
	MakeRaw() (restore func(), err error)
	// Resized はウィンドウサイズ変更の通知チャネルと、購読を終える関数を返す。
	// 購読を終えるとチャネルは閉じる。stop は1度だけ呼ぶこと。
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

// control は制御用websocketで扱うイベント。
type control struct {
	// ctx が終了したら、コンテナ内のプロセスへ終了を伝える。
	ctx context.Context
	// done はexecの完了。ハンドラを終える。
	done <-chan struct{}
	// console は端末。端末を伴わない実行ではnil。
	console Console
	// resized はウィンドウサイズ変更の通知。端末を伴わない実行ではnil。
	resized <-chan struct{}
}

// controlHandler は制御用websocketを扱うハンドラを返す。
//
// Incusは端末サイズもホスト側のシグナルも知らないため、この経路で伝える。
func controlHandler(c control) func(*websocket.Conn) {
	return func(conn *websocket.Conn) {
		defer func() {
			msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
			_ = conn.WriteMessage(websocket.CloseMessage, msg)
		}()

		c.handle(conn.WriteJSON)
	}
}

// handle は制御メッセージを送り続ける。
//
// 送信できなくなった場合は黙って終える。表示の乱れやシグナルの取りこぼしに
// とどまる一方、ここでの失敗を実行そのものの失敗として扱うと
// 利用者の作業を壊すためである。
func (c control) handle(send func(any) error) {
	for {
		select {
		case <-c.done:
			return

		case <-c.ctx.Done():
			// idev が中断された場合、コンテナ内のプロセスも止める。
			// 伝えないとパッケージ導入などが走り続け、次の実行と衝突する。
			_ = send(signalMessage(syscall.SIGTERM))
			return

		case _, ok := <-c.resized:
			if !ok {
				// 購読が終わった。以降はサイズ変更を待たない。
				c.resized = nil
				continue
			}
			width, height, err := c.console.Size()
			if err != nil {
				return
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

// Package runnertest はテスト用のRunner実装を提供する。
//
// Incus daemonを必要とせずコマンド構築を検証するために使用する
// （仕様 08-testing.md 8.1）。
package runnertest

import (
	"context"
	"fmt"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/runner"
)

// Fake は実行せずにコマンドを記録するRunner。
type Fake struct {
	// Handler が設定されていれば、応答の決定に使用される。
	Handler func(runner.Command) (runner.Result, error)
	// Stdout はコマンド文字列のprefixに対する標準出力。
	Stdout map[string]string
	// Err はコマンド文字列のprefixに対して返すエラー。
	Err map[string]error

	// Calls は実行されたコマンドの記録。
	Calls []runner.Command
}

// Run はコマンドを記録し、設定された応答を返す。
func (f *Fake) Run(_ context.Context, c runner.Command) (runner.Result, error) {
	f.Calls = append(f.Calls, c)

	if f.Handler != nil {
		return f.Handler(c)
	}

	cmd := c.String()
	for prefix, err := range f.Err {
		if strings.HasPrefix(cmd, prefix) {
			return runner.Result{ExitCode: 1}, err
		}
	}
	for prefix, out := range f.Stdout {
		if strings.HasPrefix(cmd, prefix) {
			if c.Stdout != nil {
				fmt.Fprint(c.Stdout, out)
			}
			return runner.Result{Stdout: []byte(out)}, nil
		}
	}
	return runner.Result{}, nil
}

// Commands は記録されたコマンドを文字列で返す。
func (f *Fake) Commands() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.String())
	}
	return out
}

// LastCommand は最後に実行されたコマンド文字列を返す。
func (f *Fake) LastCommand() string {
	if len(f.Calls) == 0 {
		return ""
	}
	return f.Calls[len(f.Calls)-1].String()
}

// Reset は記録を消去する。
func (f *Fake) Reset() { f.Calls = nil }

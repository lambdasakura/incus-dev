// Package runnertest はテスト用のRunner実装を提供する。
//
// Incus daemonを必要とせずコマンド構築を検証するために使用する
// （仕様 08-testing.md 8.1）。
package runnertest

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
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
	// Stdins は各コマンドへ渡された標準入力の内容（Callsと同じ順序）。
	Stdins []string
}

// Run はコマンドを記録し、設定された応答を返す。
func (f *Fake) Run(_ context.Context, c runner.Command) (runner.Result, error) {
	f.Calls = append(f.Calls, c)

	input := ""
	if c.Stdin != nil {
		if b, err := io.ReadAll(c.Stdin); err == nil {
			input = string(b)
		}
	}
	f.Stdins = append(f.Stdins, input)

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
				_, _ = fmt.Fprint(c.Stdout, out)
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

// LastArgv は最後に実行されたコマンドを、マスクせずに返す。
// 実際に実行される引数を検証するために使う。
func (f *Fake) LastArgv() string {
	if len(f.Calls) == 0 {
		return ""
	}
	c := f.Calls[len(f.Calls)-1]
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Argvs は実行されたコマンドを、マスクせずに返す。
func (f *Fake) Argvs() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

// LastCommand は最後に実行されたコマンドの表示用文字列を返す。
// Secretを含みうる値はマスクされる。
func (f *Fake) LastCommand() string {
	if len(f.Calls) == 0 {
		return ""
	}
	return f.Calls[len(f.Calls)-1].String()
}

// LastStdin は最後のコマンドへ渡された標準入力を返す。
func (f *Fake) LastStdin() string {
	if len(f.Stdins) == 0 {
		return ""
	}
	return f.Stdins[len(f.Stdins)-1]
}

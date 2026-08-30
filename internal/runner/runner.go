// Package runner は外部コマンド実行を集約する。
//
// 他のパッケージは os/exec を直接使用してはならない
// （仕様 07-implementation.md 7.2）。
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Command は実行する外部コマンド。
type Command struct {
	// Label は失敗時に表示する操作名（例: "provision step 2/3"）。
	Label string
	Name  string
	Args  []string
	// Dir は作業ディレクトリ。空ならカレントディレクトリ。
	Dir string
	// Env は追加する環境変数（KEY=VALUE）。プロセスの環境に追記される。
	Env []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Interactive が真の場合、プロセスの標準入出力を直接引き継ぐ（TTY用途）。
	Interactive bool

	// Redact は String() で値を隠す Args のindex。
	// Secretを含みうる引数（環境変数や利用者が指定した設定値）に指定する。
	// 実行される引数そのものは変わらない。
	Redact []int
}

// redacted は表示用に値を隠した引数を返す。
// KEY=VALUE 形式は KEY=*** とし、それ以外は全体を隠す。
func (c Command) redacted(i int, arg string) string {
	for _, r := range c.Redact {
		if r != i {
			continue
		}
		if key, _, ok := strings.Cut(arg, "="); ok {
			return key + "=***"
		}
		return "***"
	}
	return arg
}

// String はログ・エラー表示用のコマンド文字列を返す。
//
// 環境変数（Env）は含めず、Redact で指定された引数の値は隠す。
// Secretを無条件に出力しないための表示専用の表現であり、
// 実行される引数はこれとは独立している（仕様 04-cli.md 4.10）。
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)

	for i, a := range c.Args {
		a = c.redacted(i, a)
		if strings.ContainsAny(a, " \t\n\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Result は実行結果。
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Runner は外部コマンドを実行する。テストではfakeへ差し替える。
type Runner interface {
	Run(ctx context.Context, c Command) (Result, error)
}

// ExitError はコマンドが異常終了したことを示す。
type ExitError struct {
	Label    string
	Cmd      string
	ExitCode int
	Stderr   string
}

func (e *ExitError) Error() string {
	var sb strings.Builder
	if e.Label != "" {
		fmt.Fprintf(&sb, "%s: ", e.Label)
	}
	fmt.Fprintf(&sb, "command failed: %s (exit code %d)", e.Cmd, e.ExitCode)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		fmt.Fprintf(&sb, "\n%s", s)
	}
	return sb.String()
}

// exitCode はプロセスの終了コードを返す。
//
// シグナルで終了した場合 os は -1 を返すが、そのまま os.Exit へ渡すと
// 255 になってしまうため、シェルの慣例に合わせて 128+シグナル番号とする。
func exitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if code := state.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}

// Exec は os/exec による実装。
type Exec struct {
	Logger *slog.Logger
}

// New は既定のRunnerを返す。
func New() *Exec { return &Exec{} }

// NewWithLogger はログ出力付きのRunnerを返す。
func NewWithLogger(l *slog.Logger) *Exec { return &Exec{Logger: l} }

// Run はコマンドを実行する。
func (e *Exec) Run(ctx context.Context, c Command) (Result, error) {
	if c.Interactive {
		// 対話実行では、端末からのCtrl-Cは子プロセスへ直接届く。
		// ここで親のcontextに追従して子を殺すと、コンテナ内のコマンドを
		// 止めようとしただけでシェルごと落ちてしまう。
		ctx = context.WithoutCancel(ctx)
	}

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}

	var stdout, stderr bytes.Buffer
	switch {
	case c.Interactive:
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	default:
		cmd.Stdin = c.Stdin
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if c.Stdout != nil {
			cmd.Stdout = io.MultiWriter(&stdout, c.Stdout)
		}
		if c.Stderr != nil {
			cmd.Stderr = io.MultiWriter(&stderr, c.Stderr)
		}
	}

	if e.Logger != nil {
		e.Logger.Debug("exec", "command", c.String(), "dir", c.Dir)
	}

	err := cmd.Run()
	res := Result{
		ExitCode: exitCode(cmd.ProcessState),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	if err == nil {
		return res, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		e := &ExitError{
			Label:    c.Label,
			Cmd:      c.String(),
			ExitCode: res.ExitCode,
		}
		// 呼び出し側へ中継済みの場合、同じ内容を二重に見せない。
		if c.Stderr == nil && !c.Interactive {
			e.Stderr = string(res.Stderr)
		}
		return res, e
	}
	if c.Label != "" {
		return res, fmt.Errorf("%s: run %s: %w", c.Label, c.String(), err)
	}
	return res, fmt.Errorf("run %s: %w", c.String(), err)
}

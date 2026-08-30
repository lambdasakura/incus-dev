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
}

// String はログ・エラー表示用のコマンド文字列を返す。
// 環境変数はSecretを含みうるため含めない。
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	for _, a := range c.Args {
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
		ExitCode: cmd.ProcessState.ExitCode(),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	if err == nil {
		return res, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return res, &ExitError{
			Label:    c.Label,
			Cmd:      c.String(),
			ExitCode: res.ExitCode,
			Stderr:   string(res.Stderr),
		}
	}
	if c.Label != "" {
		return res, fmt.Errorf("%s: run %s: %w", c.Label, c.String(), err)
	}
	return res, fmt.Errorf("run %s: %w", c.String(), err)
}

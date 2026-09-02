// Package runner is where running external commands is concentrated.
//
// No other package may use os/exec directly (spec 07-implementation.md 7.2).
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
	"slices"
	"strings"
	"syscall"
	"time"
)

// Command is an external command to run.
type Command struct {
	// Label names the operation, shown on failure (e.g. "provision step 2/3").
	Label string
	Name  string
	Args  []string
	// Dir is the working directory. Empty means the current directory.
	Dir string
	// Env holds extra environment variables (KEY=VALUE), appended to the
	// process environment.
	Env []string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Interactive hands the process's own standard streams straight through,
	// for terminal use.
	Interactive bool

	// Redact lists indexes into Args whose values String() hides. Use it for
	// arguments that may carry a secret, such as environment variables and
	// user-supplied settings. The arguments actually executed are unchanged.
	Redact []int
}

// redacted returns an argument with its value hidden, for display.
//
// A marked argument is hidden whole. Showing the part before an "=" would
// have leaked any secret that contains one: a base64 value ending in "=" was
// printed in full, and so was the first line of a PEM key.
func (c Command) redacted(i int, arg string) string {
	if slices.Contains(c.Redact, i) {
		return "***"
	}
	return arg
}

// String returns the command as a string, for logs and errors.
//
// It omits the environment (Env) and hides the values of the arguments named
// by Redact. This is a display-only rendering that exists so secrets are
// never printed unconditionally; the arguments actually executed are
// independent of it (spec 04-cli.md 4.10).
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)

	for i, a := range c.Args {
		a = Collapse(c.redacted(i, a))
		if strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Collapse folds a multi-line string onto one line.
//
// Long content such as a provisioning script would bury the actual reason for
// the failure if it flowed into the error as it is, so only the first line is
// shown.
func Collapse(arg string) string {
	first, rest, found := strings.Cut(arg, "\n")
	if !found {
		return arg
	}

	lines := strings.Count(strings.TrimRight(rest, "\n"), "\n") + 1
	return fmt.Sprintf("%s … (+%d lines)", first, lines)
}

// Result is the outcome of a run.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Runner runs external commands. Tests replace it with a fake.
type Runner interface {
	Run(ctx context.Context, c Command) (Result, error)
}

// ExitError reports that a command terminated abnormally.
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

// exitCode returns the process exit code.
//
// os reports -1 for a process killed by a signal, which would turn into 255 if
// passed straight to os.Exit, so follow the shell convention of 128+signal.
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

// Exec is the os/exec-backed implementation.
type Exec struct {
	Logger *slog.Logger
}

// New returns the default Runner.
func New() *Exec { return &Exec{} }

// NewWithLogger returns a Runner that logs what it runs.
func NewWithLogger(l *slog.Logger) *Exec { return &Exec{Logger: l} }

// waitDelay bounds how long an interrupted command is given to finish
// streaming its output.
//
// Killing the command does not kill what it started, and a surviving
// grandchild holds the output pipe open. Without a bound, Run would wait for
// it — leaving the ansible step's temporary files, secrets included, on disk
// for as long as that lasted.
const waitDelay = 2 * time.Second

// Run executes the command.
func (e *Exec) Run(ctx context.Context, c Command) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.WaitDelay = waitDelay
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}

	// Stream straight to the caller's writers when it gave us any. Some steps
	// produce an unpredictable amount of output, so do not accumulate it
	// needlessly.
	var stdout, stderr bytes.Buffer
	cmd.Stdin = c.Stdin

	cmd.Stdout = c.Stdout
	if cmd.Stdout == nil {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = c.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = &stderr
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

	// The command was killed because the run was interrupted, so it did not
	// fail on its own account. Reported as a failure, the user is told their
	// playbook broke when they are the one who stopped it.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if c.Label == "" {
			return res, ctxErr
		}
		return res, fmt.Errorf("%s: %w", c.Label, ctxErr)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		e := &ExitError{
			Label:    c.Label,
			Cmd:      c.String(),
			ExitCode: res.ExitCode,
		}
		// Do not show the same content twice when it was already streamed.
		if c.Stderr == nil {
			e.Stderr = string(res.Stderr)
		}
		return res, e
	}
	if c.Label != "" {
		return res, fmt.Errorf("%s: run %s: %w", c.Label, c.String(), err)
	}
	return res, fmt.Errorf("run %s: %w", c.String(), err)
}

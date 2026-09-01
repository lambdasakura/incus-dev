// Package runnertest provides a Runner implementation for tests.
//
// Use it to verify how commands are built without needing an Incus daemon
// (spec 08-testing.md 8.1).
package runnertest

import (
	"context"
	"fmt"
	"io"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// Fake is a Runner that records commands instead of running them.
type Fake struct {
	// Handler, when set, decides the response.
	Handler func(runner.Command) (runner.Result, error)
	// Stdout maps a command-string prefix to the standard output to return.
	Stdout map[string]string
	// Err maps a command-string prefix to the error to return.
	Err map[string]error

	// Calls records the commands that were run.
	Calls []runner.Command
	// Stdins holds the standard input given to each command, in the same order
	// as Calls.
	Stdins []string
}

// Run records the command and returns the configured response.
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

// Commands returns the recorded commands as strings.
func (f *Fake) Commands() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.String())
	}
	return out
}

// LastArgv returns the last command run, unmasked.
// Use it to verify the arguments that are actually executed.
func (f *Fake) LastArgv() string {
	if len(f.Calls) == 0 {
		return ""
	}
	c := f.Calls[len(f.Calls)-1]
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Argvs returns the commands that were run, unmasked.
func (f *Fake) Argvs() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

// LastCommand returns the display string of the last command run.
// Values that may be secrets are masked.
func (f *Fake) LastCommand() string {
	if len(f.Calls) == 0 {
		return ""
	}
	return f.Calls[len(f.Calls)-1].String()
}

// LastStdin returns the standard input given to the last command.
func (f *Fake) LastStdin() string {
	if len(f.Stdins) == 0 {
		return ""
	}
	return f.Stdins[len(f.Stdins)-1]
}

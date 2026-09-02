// Command idev builds and manages per-project development environments with Incus.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lambdasakura/incus-dev/internal/cli"
)

// version is stamped in at build time with -ldflags.
var version = "dev"

// osExit is an indirection so tests can replace it.
var osExit = os.Exit

func main() {
	osExit(run(os.Args[1:], os.Stderr))
}

// run executes the CLI and returns the process exit code.
// It never calls os.Exit, so tests can call it.
func run(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer exitOnNextSignal(ctx, os.Interrupt, syscall.SIGTERM)()

	return cli.Report(stderr, cli.Execute(ctx, version, args))
}

// exitOnNextSignal ends the process on the second interrupt, and returns a
// function that stops watching.
//
// NotifyContext keeps the handler installed until stop(), but the goroutine
// behind it delivers only the first signal; every later one lands in a channel
// nobody reads. Several Incus calls take no context -- the client waits an
// hour on a daemon that accepts and never answers -- so without this the
// second Ctrl-C does nothing and the only way out is SIGKILL, which skips
// every defer and can leave the terminal in raw mode.
//
// It catches the next signal rather than handing it back: neither
// signal.Stop nor signal.Reset restores the terminating disposition once the
// runtime has taken the signal over -- both leave it disabled, and the process
// goes on ignoring Ctrl-C. Measured, not assumed.
func exitOnNextSignal(ctx context.Context, signals ...os.Signal) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}

		next := make(chan os.Signal, 1)
		signal.Notify(next, signals...)
		defer signal.Stop(next)

		select {
		case received := <-next:
			// What the shell reports for a process killed by a signal, so a
			// caller sees the same code it would have seen without idev
			// catching it at all.
			code := 1
			if sig, ok := received.(syscall.Signal); ok {
				code = 128 + int(sig)
			}
			osExit(code)
		case <-done:
		}
	}()
	return func() { close(done) }
}

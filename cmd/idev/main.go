// Command idev builds and manages per-project development environments with Incus.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lambdasakura/incus-devkit/internal/cli"
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

	return cli.Report(stderr, cli.Execute(ctx, version, args))
}

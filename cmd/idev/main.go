// Command idev はIncusでプロジェクト単位の開発環境を構築・管理する。
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lambdasakura/incus-dev/internal/cli"
)

// version はビルド時に -ldflags で埋め込む。
var version = "dev"

// osExit はテストから差し替えるための間接参照。
var osExit = os.Exit

func main() {
	osExit(run(os.Args[1:], os.Stderr))
}

// run はCLIを実行し、プロセスの終了コードを返す。
// os.Exit を呼ばないため、テストから実行できる。
func run(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return cli.Report(stderr, cli.Execute(ctx, version, args))
}

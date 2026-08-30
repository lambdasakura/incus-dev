// Command idev はIncusでプロジェクト単位の開発環境を構築・管理する。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/cli"
)

// version はビルド時に -ldflags で埋め込む。
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// run はCLIを実行し、プロセスの終了コードを返す。
// os.Exit を呼ばないため、テストから実行できる。
func run(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.Execute(ctx, version, args)
	if err == nil {
		return 0
	}

	// コンテナ内コマンドが異常終了しただけの場合は、
	// その終了コードをそのまま返す（出力は既に中継済み）。
	var exitErr *cli.ExitCodeError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	_, _ = fmt.Fprintf(stderr, "[idev] error: %v\n", err)
	return 1
}

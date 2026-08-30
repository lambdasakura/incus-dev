// Command idev はIncusでプロジェクト単位の開発環境を構築・管理する。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lambdasakura/incus-dev/internal/cli"
)

// version はビルド時に -ldflags で埋め込む。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx, version, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "[idev] error: %v\n", err)

		// コンテナ内コマンドの終了コードはそのまま伝播させる。
		var exitErr *cli.ExitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		os.Exit(1)
	}
}

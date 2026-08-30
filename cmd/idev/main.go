// Command idev はIncusでプロジェクト単位の開発環境を構築・管理する。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/cli"
)

// version はビルド時に -ldflags で埋め込む。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx, version, os.Args[1:]); err != nil {
		// コンテナ内コマンドが異常終了しただけの場合は、
		// その終了コードをそのまま返す（出力は既に中継済み）。
		var exitErr *cli.ExitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}

		fmt.Fprintf(os.Stderr, "[idev] error: %v\n", err)
		os.Exit(1)
	}
}

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// branchFunc は現在のGitブランチ名を返す。
type branchFunc func() (string, error)

// instanceNameFor は project.scope に従ってinstance名を決める
// （仕様 05-incus.md 5.1）。
//
// 既定は従来どおりプロジェクト名のみ。同一マシンで複数のチェックアウトを
// 扱う場合に、パスやブランチで区別できるようにする。
func instanceNameFor(cfg *config.Config, branch branchFunc) (string, error) {
	switch cfg.Project.ScopeOrDefault() {
	case config.ScopePath:
		// チェックアウト先ごとに安定した接尾辞にする。
		return incus.InstanceNameWithSuffix(cfg.Project.Name, incus.ShortHash(cfg.Root)), nil

	case config.ScopeBranch:
		if branch == nil {
			return "", fmt.Errorf("project.scope: branch requires git")
		}
		name, err := branch()
		if err != nil {
			return "", fmt.Errorf("project.scope: branch requires the current git branch: %w", err)
		}
		return incus.InstanceNameWithSuffix(cfg.Project.Name, name), nil

	default:
		return incus.InstanceName(cfg.Project.Name), nil
	}
}

// gitBranch は project root の現在のブランチ名を返す。
//
// symbolic-ref はコミットが無いリポジトリでもブランチ名を返す。
// detached HEAD では失敗するため、その場合はコミットの短縮ハッシュを使う。
func gitBranch(ctx context.Context, r runner.Runner, root string) branchFunc {
	return func() (string, error) {
		for _, args := range [][]string{
			{"-C", root, "symbolic-ref", "--short", "HEAD"},
			{"-C", root, "rev-parse", "--short", "HEAD"},
		} {
			res, err := r.Run(ctx, runner.Command{Name: "git", Args: args})
			if err != nil {
				continue
			}
			if name := strings.TrimSpace(string(res.Stdout)); name != "" {
				return name, nil
			}
		}
		return "", fmt.Errorf("could not determine the git branch of %s", root)
	}
}

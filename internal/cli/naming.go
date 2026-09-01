package cli

import (
	"context"
	"fmt"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// branchFunc returns the current Git branch name.
type branchFunc func() (string, error)

// instanceNameFor derives the instance name according to project.scope
// (spec 05-incus.md 5.1).
//
// The default is the project name alone, as before. Several checkouts on one
// machine can be told apart by path or by branch.
func instanceNameFor(cfg *config.Config, branch branchFunc) (string, error) {
	switch cfg.Project.ScopeOrDefault() {
	case config.ScopePath:
		// A suffix that is stable per checkout.
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

// gitBranch returns the current branch name at the project root.
//
// symbolic-ref gives a branch name even in a repository with no commits. It
// fails on a detached HEAD, where the commit's short hash is used instead.
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

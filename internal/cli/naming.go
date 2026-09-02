package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// branchFunc returns the current Git branch name.
type branchFunc func() (string, error)

// instanceNameFor derives the instance name according to project.scope
// (spec 05-incus.md 5.1).
//
// The default is path, which gives one instance per checkout. name puts every
// checkout of a project on one instance, and branch gives one per branch.
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
		// The first failure, kept: git not being installed, the directory not
		// being a repository and git exiting non-zero send the user to three
		// different places, and every command that names the instance is dead
		// until they know which one it is.
		var why error
		for _, args := range [][]string{
			{"-C", root, "symbolic-ref", "--short", "HEAD"},
			{"-C", root, "rev-parse", "--short", "HEAD"},
		} {
			res, err := r.Run(ctx, runner.Command{Name: "git", Args: args})
			if err != nil {
				if why == nil {
					why = err
				}
				continue
			}
			if name := strings.TrimSpace(string(res.Stdout)); name != "" {
				return name, nil
			}
		}
		if why != nil {
			return "", fmt.Errorf("could not determine the git branch of %s: %w", root, why)
		}
		return "", fmt.Errorf("could not determine the git branch of %s", root)
	}
}

package provision

import (
	"context"

	"github.com/lambdasakura/incus-devkit/internal/config"
	"github.com/lambdasakura/incus-devkit/internal/runner"
)

// execGalaxy runs ansible-galaxy install on the host.
//
// It lets a project install its roles and collections entirely from within
// .incus-dev/ (spec 06-provisioning.md 6.5.5). Where they land is Ansible's
// default.
func (e *Executor) execGalaxy(ctx context.Context, step *config.GalaxyStep, env Env) error {
	if err := e.checkPrerequisites(ctx); err != nil {
		return err
	}

	args := runner.Args("install", "-r", resolve(env.ProjectRoot, step.Requirements))
	args.AddSecret(step.ExtraArgs...)

	cmd := runner.Command{
		Name:   "ansible-galaxy",
		Dir:    env.ProjectRoot,
		Stdout: e.Stdout,
		Stderr: e.Stderr,
	}
	args.Apply(&cmd)

	_, err := e.Runner.Run(ctx, cmd)
	return err
}

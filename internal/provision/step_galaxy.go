package provision

import (
	"context"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// execGalaxy runs ansible-galaxy install on the host.
//
// It lets a project install its roles and collections entirely from within
// .incus-dev/ (spec 06-provisioning.md 6.5.5). Where they land is what the
// project's ansible.cfg says, which is where its playbooks look.
func (e *Executor) execGalaxy(ctx context.Context, step *config.GalaxyStep, env Env) error {
	if err := e.checkGalaxy(ctx, env.ProjectRoot); err != nil {
		return err
	}

	args := runner.Args("install", "-r", resolve(env.ProjectRoot, step.Requirements))
	args.AddSecret(step.ExtraArgs...)

	cmd := runner.Command{
		Name: "ansible-galaxy",
		Dir:  env.ProjectRoot,
		// The same configuration the playbook step reads, so roles and
		// collections land where the playbook will look for them. Without it
		// they went to Ansible's default while the playbook read the
		// project's roles_path, and the step after this one failed on a role
		// this one had just reported installing.
		Env:    ansibleEnv(env.ProjectRoot),
		Stdout: e.Stdout,
		Stderr: e.Stderr,
	}
	args.Apply(&cmd)

	_, err := e.Runner.Run(ctx, cmd)
	return err
}

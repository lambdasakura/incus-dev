package provision

import (
	"context"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// execGalaxy はホスト側で ansible-galaxy install を実行する。
//
// Roleやcollectionの導入を .incus-dev/ 内で完結させるためのステップ
// （仕様 06-provisioning.md 6.5.5）。導入先はansibleの既定に従う。
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

package provision

import (
	"context"
	"fmt"
	"strconv"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// execRun runs a script inside the container (spec 06-provisioning.md 6.4).
func (e *Executor) execRun(ctx context.Context, step *config.RunStep, env Env) error {
	// The variables devkit injects help with diagnosis and are safe to show.
	// Values the project supplied may be secrets, so they are hidden.
	public := env.EnvVars()
	secret := make(map[string]string, len(step.Env)+len(env.Secrets))
	for k, v := range env.Secrets {
		secret[k] = v
	}
	for k, v := range step.Env {
		delete(public, k) // what the project set wins
		secret[k] = v
	}

	argv, user := runArgv(step)

	code, err := e.Incus.Exec(ctx, env.Instance, argv, incus.ExecOptions{
		Env:       secret,
		PublicEnv: public,
		Cwd:       step.Cwd,
		User:      user,
		Stdout:    e.Stdout,
		Stderr:    e.Stderr,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		// Say which script failed (spec 04-cli.md 4.10). Never include env,
		// whose values may be secrets.
		return fmt.Errorf("%s: exited with code %d", runner.Collapse(step.Script), code)
	}
	return nil
}

// runArgv returns the argv to run inside the container, and the user to pass
// to Incus.
//
// The Incus exec API only accepts a uid — it does not resolve user names — so
// a name is switched to with su, and nothing is passed to Incus.
func runArgv(step *config.RunStep) (argv []string, user string) {
	shell := step.ShellOrDefault()

	if step.User != "" {
		if _, err := strconv.Atoi(step.User); err != nil {
			return []string{"su", "-s", shell, step.User, "-c", step.Script}, ""
		}
	}
	return []string{shell, "-c", step.Script}, step.User
}

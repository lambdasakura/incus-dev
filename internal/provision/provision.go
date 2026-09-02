package provision

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// defaultBootstrapScript is the bootstrap used when provision has an ansible
// step.
//
// Ansible modules need Python in the container, which is why idev carries
// this one thing. It assumes a Debian-family image; on any other OS the
// project must declare bootstrap itself (spec 06-provisioning.md 6.3.2 — the
// sole exception to REQ-007).
const defaultBootstrapScript = `command -v python3 >/dev/null 2>&1 || ` +
	`(apt-get update && apt-get install -y python3)`

// DefaultBootstrapName is the display name of the default bootstrap step.
const DefaultBootstrapName = "bootstrap (default)"

// defaultBootstrapHint is what to tell the user when the default bootstrap
// fails.
const defaultBootstrapHint = `The default bootstrap assumes a Debian-family image (apt-get).
Define bootstrap explicitly in dev.yml for this image, for example:

  bootstrap:
    - run: command -v python3 >/dev/null 2>&1 || dnf install -y python3

Use "bootstrap: []" to skip it entirely.`

// BootstrapSteps returns the bootstrap steps to run.
//
//   - Declared bootstrap wins (an empty list disables it)
//   - Omitted, the default bootstrap is used when there is an ansible step
//   - Otherwise nothing runs
func BootstrapSteps(cfg *config.Config) []config.Step {
	if cfg.Bootstrap != nil {
		return *cfg.Bootstrap
	}
	if !cfg.HasAnsibleStep() {
		return nil
	}
	return []config.Step{{
		Name: DefaultBootstrapName,
		Run:  &config.RunStep{Script: defaultBootstrapScript},
	}}
}

// Executor runs steps.
type Executor struct {
	Incus  incus.Client
	Runner runner.Runner
	Logger *slog.Logger
	// Stdout and Stderr are where step output is streamed. nil discards it.
	Stdout io.Writer
	Stderr io.Writer

	// The prerequisites for ansible steps are checked once.
	ansibleCheck sync.Once
	ansibleErr   error
}

// Bootstrap runs the bootstrap steps.
func (e *Executor) Bootstrap(ctx context.Context, cfg *config.Config, env Env) error {
	return e.RunSteps(ctx, BootstrapSteps(cfg), "bootstrap", env)
}

// Provision runs the provision steps, or only the subset sel names.
func (e *Executor) Provision(ctx context.Context, cfg *config.Config, env Env, sel Selection) error {
	indices, err := Select(cfg.Provision, sel)
	if err != nil {
		return err
	}
	return e.runSteps(ctx, cfg.Provision, indices, "provision", env)
}

// RunSteps runs the steps in order, stopping at the first failure.
func (e *Executor) RunSteps(ctx context.Context, steps []config.Step, kind string, env Env) error {
	indices := make([]int, len(steps))
	for i := range steps {
		indices[i] = i
	}
	return e.runSteps(ctx, steps, indices, kind, env)
}

// runSteps runs the steps at the given indices, in declaration order.
//
// The label shows the position within the whole list. Showing "step 1/1" for a
// partial run would leave no way to tell which step it was.
func (e *Executor) runSteps(ctx context.Context, steps []config.Step, indices []int, kind string, env Env) error {
	total := len(steps)
	for _, i := range indices {
		step := steps[i]
		label := fmt.Sprintf("%s step %d/%d: %s", kind, i+1, total, step.DisplayName(i+1))
		e.log(label)

		if err := e.runStep(ctx, step, env); err != nil {
			if step.Name == DefaultBootstrapName {
				// The default bootstrap assumes a Debian family. When it fails,
				// tell the project to declare its own (spec 06-provisioning.md
				// 6.3.2).
				return fmt.Errorf("%s: %w\n\n%s", label, err, defaultBootstrapHint)
			}
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func (e *Executor) runStep(ctx context.Context, step config.Step, env Env) error {
	switch {
	case step.Run != nil:
		return e.execRun(ctx, step.Run, env)
	case step.Ansible != nil:
		return e.execAnsible(ctx, step.Ansible, env)
	case step.Galaxy != nil:
		return e.execGalaxy(ctx, step.Galaxy, env)
	default:
		return fmt.Errorf("step has neither run nor ansible")
	}
}

func (e *Executor) log(msg string, args ...any) {
	if e.Logger != nil {
		e.Logger.Info(msg, args...)
	}
}

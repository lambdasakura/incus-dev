package provision

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// defaultBootstrapScript は provision に ansible ステップがある場合の既定bootstrap。
//
// Ansible Moduleの実行にはコンテナ内のPythonが必要なため、これのみdevkitが持つ。
// Debian系イメージを前提とするため、他のOSではプロジェクト側で bootstrap を
// 明示すること（仕様 06-provisioning.md 6.3.2、REQ-007の唯一の例外）。
const defaultBootstrapScript = `command -v python3 >/dev/null 2>&1 || ` +
	`(apt-get update && apt-get install -y python3)`

// DefaultBootstrapName は既定bootstrapステップの表示名。
const DefaultBootstrapName = "bootstrap (default)"

// BootstrapSteps は実行すべきbootstrapステップを返す。
//
//   - bootstrap が明示されていればそれを使う（空リストは無効化）
//   - 省略時、ansible ステップがあれば既定bootstrapを使う
//   - それ以外は何もしない
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

// Executor はステップを実行する。
type Executor struct {
	Incus  incus.Client
	Runner runner.Runner
	Logger *slog.Logger
	// Stdout / Stderr はステップ出力の中継先。nilの場合は破棄する。
	Stdout io.Writer
	Stderr io.Writer
}

// Bootstrap はbootstrapステップを実行する。
func (e *Executor) Bootstrap(ctx context.Context, cfg *config.Config, env Env) error {
	return e.RunSteps(ctx, BootstrapSteps(cfg), "bootstrap", env)
}

// Provision はprovisionステップを実行する。
func (e *Executor) Provision(ctx context.Context, cfg *config.Config, env Env) error {
	return e.RunSteps(ctx, cfg.Provision, "provision", env)
}

// RunSteps はステップを順に実行する。失敗した時点で後続を実行しない。
func (e *Executor) RunSteps(ctx context.Context, steps []config.Step, kind string, env Env) error {
	total := len(steps)
	for i, step := range steps {
		label := fmt.Sprintf("%s step %d/%d: %s", kind, i+1, total, step.DisplayName(i+1))
		e.log(label)

		if err := e.runStep(ctx, step, label, env); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	return nil
}

func (e *Executor) runStep(ctx context.Context, step config.Step, label string, env Env) error {
	switch {
	case step.Run != nil:
		return e.execRun(ctx, step.Run, label, env)
	case step.Ansible != nil:
		return e.execAnsible(ctx, step.Ansible, label, env)
	default:
		return fmt.Errorf("step has neither run nor ansible")
	}
}

func (e *Executor) log(msg string, args ...any) {
	if e.Logger != nil {
		e.Logger.Info(msg, args...)
	}
}

package provision

import (
	"context"
	"fmt"
	"strconv"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
)

// execRun はコンテナ内でスクリプトを実行する（仕様 06-provisioning.md 6.4）。
func (e *Executor) execRun(ctx context.Context, step *config.RunStep, env Env) error {
	// devkitが注入する変数は診断に役立つため表示してよい。
	// プロジェクトが指定した値はSecretを含みうるため隠す。
	public := env.EnvVars()
	secret := make(map[string]string, len(step.Env))
	for k, v := range step.Env {
		delete(public, k) // プロジェクト指定を優先する
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
		return fmt.Errorf("exited with code %d", code)
	}
	return nil
}

// runArgv はコンテナ内で実行するargvと、incusへ渡すユーザー指定を返す。
//
// incus exec --user はUIDのみを受け付けるため、ユーザー名が指定された場合は
// su でユーザーを切り替え、incusへは何も渡さない。
func runArgv(step *config.RunStep) (argv []string, user string) {
	shell := step.ShellOrDefault()

	if step.User != "" {
		if _, err := strconv.Atoi(step.User); err != nil {
			return []string{"su", "-s", shell, step.User, "-c", step.Script}, ""
		}
	}
	return []string{shell, "-c", step.Script}, step.User
}

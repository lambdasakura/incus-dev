package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/project"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

const (
	// InventoryHost は生成するinventory上のホスト名（仕様 06-provisioning.md 6.5.2）。
	InventoryHost = "dev"
	// InventoryGroup は生成するinventory上のグループ名。
	InventoryGroup = "devkit"
	// ConnectionPlugin はコンテナへの接続に使用するconnection plugin。
	ConnectionPlugin = "community.general.incus"
)

// execAnsible はホスト側で ansible-playbook を実行する（仕様 06-provisioning.md 6.5）。
func (e *Executor) execAnsible(ctx context.Context, step *config.AnsibleStep, env Env) error {
	dir, err := os.MkdirTemp("", "idev-ansible-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	inventoryPath := filepath.Join(dir, "inventory.yml")
	if err := writeYAML(inventoryPath, inventoryFor(env)); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	varsPath := filepath.Join(dir, "devkit-vars.json")
	if err := writeJSON(varsPath, env.AnsibleVars()); err != nil {
		return fmt.Errorf("write devkit vars: %w", err)
	}

	args := runner.Args("-i", inventoryPath)
	if step.Inventory != "" {
		args.Add("-i", resolve(env.ProjectRoot, step.Inventory))
	}
	// devkitの変数を先に渡し、プロジェクト側での上書きを許す。
	args.Add("--extra-vars=@" + varsPath)
	if step.Vars != "" {
		args.Add("--extra-vars=@" + resolve(env.ProjectRoot, step.Vars))
	}
	if len(step.Tags) > 0 {
		args.Add("--tags", strings.Join(step.Tags, ","))
	}
	if len(step.SkipTags) > 0 {
		args.Add("--skip-tags", strings.Join(step.SkipTags, ","))
	}
	// extra_args は ansible-playbook へ素通しするため、
	// -e で秘密の変数を渡すといった使い方がありうる。
	args.AddSecret(step.ExtraArgs...)
	args.Add(resolve(env.ProjectRoot, step.Playbook))

	// ラベルは RunSteps 側で付与するため、ここでは付けない（二重表示になる）。
	cmd := runner.Command{
		Name:   "ansible-playbook",
		Dir:    env.ProjectRoot,
		Env:    ansibleEnv(env.ProjectRoot),
		Stdout: e.Stdout,
		Stderr: e.Stderr,
	}
	args.Apply(&cmd)

	_, err = e.Runner.Run(ctx, cmd)
	return err
}

// inventoryFor はdevkitが生成する一時inventoryの内容を組み立てる。
func inventoryFor(env Env) map[string]any {
	host := map[string]any{
		"ansible_host":          env.Instance,
		"ansible_connection":    ConnectionPlugin,
		"ansible_incus_remote":  orDefault(env.Remote, "local"),
		"ansible_incus_project": orDefault(env.IncusProject, "default"),
	}
	return map[string]any{
		"all": map[string]any{
			"children": map[string]any{
				InventoryGroup: map[string]any{
					"hosts": map[string]any{InventoryHost: host},
				},
			},
		},
	}
}

// writeYAML は値をYAMLとして書き出す。一時ファイルなので所有者のみ読める。
func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// writeJSON は値をJSONとして書き出す。
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ansibleEnv はプロジェクトの ansible.cfg があればそれを使わせる（仕様 6.5.3）。
func ansibleEnv(root string) []string {
	cfg := filepath.Join(root, project.ConfigDir, "ansible", "ansible.cfg")
	if _, err := os.Stat(cfg); err != nil {
		return nil
	}
	return []string{"ANSIBLE_CONFIG=" + cfg}
}

func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

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
func (e *Executor) execAnsible(ctx context.Context, step *config.AnsibleStep, label string, env Env) error {
	dir, err := os.MkdirTemp("", "idev-ansible-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	inventoryPath := filepath.Join(dir, "inventory.yml")
	if err := writeInventory(inventoryPath, env); err != nil {
		return err
	}
	varsPath := filepath.Join(dir, "devkit-vars.json")
	if err := writeVars(varsPath, env); err != nil {
		return err
	}

	args := []string{"-i", inventoryPath}
	if step.Inventory != "" {
		args = append(args, "-i", resolve(env.ProjectRoot, step.Inventory))
	}
	// devkitの変数を先に渡し、プロジェクト側での上書きを許す。
	args = append(args, "--extra-vars=@"+varsPath)
	if step.Vars != "" {
		args = append(args, "--extra-vars=@"+resolve(env.ProjectRoot, step.Vars))
	}
	if len(step.Tags) > 0 {
		args = append(args, "--tags", strings.Join(step.Tags, ","))
	}
	if len(step.SkipTags) > 0 {
		args = append(args, "--skip-tags", strings.Join(step.SkipTags, ","))
	}
	args = append(args, step.ExtraArgs...)
	args = append(args, resolve(env.ProjectRoot, step.Playbook))

	_, err = e.Runner.Run(ctx, runner.Command{
		Label:  label,
		Name:   "ansible-playbook",
		Args:   args,
		Dir:    env.ProjectRoot,
		Env:    ansibleEnv(env.ProjectRoot),
		Stdout: e.Stdout,
		Stderr: e.Stderr,
	})
	return err
}

// writeInventory はdevkitが生成する一時inventoryを書き出す。
func writeInventory(path string, env Env) error {
	host := map[string]any{
		"ansible_host":          env.Instance,
		"ansible_connection":    ConnectionPlugin,
		"ansible_incus_remote":  orDefault(env.Remote, "local"),
		"ansible_incus_project": orDefault(env.IncusProject, "default"),
	}
	inventory := map[string]any{
		"all": map[string]any{
			"children": map[string]any{
				InventoryGroup: map[string]any{
					"hosts": map[string]any{InventoryHost: host},
				},
			},
		},
	}

	data, err := yaml.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("build inventory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	return nil
}

func writeVars(path string, env Env) error {
	data, err := json.Marshal(env.AnsibleVars())
	if err != nil {
		return fmt.Errorf("build devkit vars: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write devkit vars: %w", err)
	}
	return nil
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

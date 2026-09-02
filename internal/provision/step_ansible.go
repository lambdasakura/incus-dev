package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/lambdasakura/incus-devkit/internal/config"
	"github.com/lambdasakura/incus-devkit/internal/project"
	"github.com/lambdasakura/incus-devkit/internal/runner"
)

const (
	// InventoryHost is the host name in the generated inventory
	// (spec 06-provisioning.md 6.5.2).
	InventoryHost = "dev"
	// InventoryGroup is the group name in the generated inventory.
	InventoryGroup = "devkit"
	// ConnectionPlugin is the connection plugin used to reach the container.
	ConnectionPlugin = "community.general.incus"
)

// checkPrerequisites verifies, once, that the prerequisites for ansible steps
// are in place.
//
// Without them, ansible-playbook's own output makes the cause hard to see, so
// stop early and say what to do (spec 06-provisioning.md 6.5.1).
func (e *Executor) checkPrerequisites(ctx context.Context) error {
	e.ansibleCheck.Do(func() {
		if _, err := e.Runner.Run(ctx, runner.Command{
			Name: "ansible-playbook",
			Args: []string{"--version"},
		}); err != nil {
			e.ansibleErr = fmt.Errorf(
				"ansible-playbook is required for ansible steps but could not be run: %w\n"+
					"install Ansible on this host, or use run steps instead", err)
			return
		}

		if _, err := e.Runner.Run(ctx, runner.Command{
			Name: "ansible-doc",
			Args: []string{"-t", "connection", ConnectionPlugin},
		}); err != nil {
			e.ansibleErr = fmt.Errorf(
				"the %s connection plugin is required but was not found: %w\n"+
					"install it with: ansible-galaxy collection install community.general", ConnectionPlugin, err)
		}
	})
	return e.ansibleErr
}

// execAnsible runs ansible-playbook on the host (spec 06-provisioning.md 6.5).
func (e *Executor) execAnsible(ctx context.Context, step *config.AnsibleStep, env Env) error {
	if err := e.checkPrerequisites(ctx); err != nil {
		return err
	}

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

	// Secrets go in a file of their own: a mode 0600 temporary file, deleted
	// once the run is over.
	secretsPath := filepath.Join(dir, "secrets.json")
	if len(env.Secrets) > 0 {
		if err := writeJSON(secretsPath, env.Secrets); err != nil {
			return fmt.Errorf("write secrets: %w", err)
		}
	}

	args := runner.Args("-i", inventoryPath)
	if step.Inventory != "" {
		args.Add("-i", resolve(env.ProjectRoot, step.Inventory))
	}
	// Pass devkit's variables first, so the project can override them.
	args.Add("--extra-vars=@" + varsPath)
	if len(env.Secrets) > 0 {
		args.Add("--extra-vars=@" + secretsPath)
	}
	if step.Vars != "" {
		args.Add("--extra-vars=@" + resolve(env.ProjectRoot, step.Vars))
	}
	if len(step.Tags) > 0 {
		args.Add("--tags", strings.Join(step.Tags, ","))
	}
	if len(step.SkipTags) > 0 {
		args.Add("--skip-tags", strings.Join(step.SkipTags, ","))
	}
	// extra_args passes straight to ansible-playbook, so it may well carry a
	// secret variable through -e.
	args.AddSecret(step.ExtraArgs...)
	args.Add(resolve(env.ProjectRoot, step.Playbook))

	// RunSteps adds the label, so adding one here would show it twice.
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

// inventoryFor builds the contents of the temporary inventory devkit generates.
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

// writeYAML writes a value as YAML. It is a temporary file, readable only by
// its owner.
func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// writeJSON writes a value as JSON.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ansibleEnv points Ansible at the project's ansible.cfg when it has one
// (spec 6.5.3).
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

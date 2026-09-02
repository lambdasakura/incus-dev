package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

const (
	// InventoryHost is the host name in the generated inventory
	// (spec 06-provisioning.md 6.5.2).
	InventoryHost = "dev"
	// InventoryGroup is the group name in the generated inventory.
	InventoryGroup = "idev"
	// ConnectionPlugin is the connection plugin used to reach the container.
	ConnectionPlugin = "community.general.incus"
)

// CheckPrerequisites verifies what the given steps need on the host, before
// anything is done to the instance.
//
// Left to the step itself, the check happens after the instance has been
// created, started and bootstrapped, so the user waits through all of that to
// be told Ansible is not installed (spec 06-provisioning.md 6.5.1).
func (e *Executor) CheckPrerequisites(ctx context.Context, root string, steps []config.Step) error {
	var hasAnsible, hasGalaxy, galaxyFirst bool
	for _, step := range steps {
		switch {
		case step.Galaxy != nil:
			// Only a galaxy step that runs before the first ansible step can
			// install what that step needs.
			galaxyFirst = galaxyFirst || !hasAnsible
			hasGalaxy = true
		case step.Ansible != nil:
			hasAnsible = true
		}
	}

	if hasGalaxy {
		if err := e.checkGalaxy(ctx, root); err != nil {
			return err
		}
	}
	if !hasAnsible {
		return nil
	}
	if err := e.checkPlaybook(ctx, root); err != nil {
		return err
	}
	if galaxyFirst {
		// The connection plugin is in community.general, which is what that
		// galaxy step is usually there to install. Requiring it now would make
		// that impossible, so leave it to the ansible step itself.
		return nil
	}
	return e.checkPlugin(ctx, root)
}

// checkPlaybook verifies, once, that ansible-playbook can be run.
//
// Without it, ansible's own output makes the cause hard to see, so stop early
// and say what to do (spec 06-provisioning.md 6.5.1).
func (e *Executor) checkPlaybook(ctx context.Context, root string) error {
	e.playbookCheck.Do(func() {
		if _, err := e.Runner.Run(ctx, runner.Command{
			Name: "ansible-playbook",
			Args: []string{"--version"},
			Env:  ansibleEnv(root),
		}); err != nil {
			e.playbookErr = fmt.Errorf(
				"ansible-playbook is required for ansible steps but could not be run: %w\n"+
					"install Ansible on this host, or use run steps instead", err)
		}
	})
	return e.playbookErr
}

// checkPlugin verifies, once, that the connection plugin is installed.
//
// ansible-doc exits 0 for a plugin it cannot find, writing its warning to
// standard error, so the exit code says only whether ansible-doc itself ran.
// What distinguishes the two is the documentation: none is printed for a
// plugin that is not there.
func (e *Executor) checkPlugin(ctx context.Context, root string) error {
	e.pluginCheck.Do(func() {
		res, err := e.Runner.Run(ctx, runner.Command{
			Name: "ansible-doc",
			Args: []string{"-t", "connection", ConnectionPlugin},
			// The configuration the step will use, so a plugin installed in
			// the project's own collections_path is found where it lives.
			Env: ansibleEnv(root),
		})
		switch {
		case err != nil:
			e.pluginErr = fmt.Errorf(
				"ansible-doc is required to check for the %s connection plugin "+
					"but could not be run: %w\ninstall Ansible on this host, or use run steps instead",
				ConnectionPlugin, err)
		case len(bytes.TrimSpace(res.Stdout)) == 0:
			e.pluginErr = fmt.Errorf(
				"the %s connection plugin is required but was not found\n"+
					"install it with: ansible-galaxy collection install community.general", ConnectionPlugin)
		}
	})
	return e.pluginErr
}

// checkGalaxy verifies, once, that ansible-galaxy can be run.
func (e *Executor) checkGalaxy(ctx context.Context, root string) error {
	e.galaxyCheck.Do(func() {
		if _, err := e.Runner.Run(ctx, runner.Command{
			Name: "ansible-galaxy",
			Args: []string{"--version"},
			Env:  ansibleEnv(root),
		}); err != nil {
			e.galaxyErr = fmt.Errorf(
				"ansible-galaxy is required for galaxy steps but could not be run: %w\n"+
					"install Ansible on this host", err)
		}
	})
	return e.galaxyErr
}

// execAnsible runs ansible-playbook on the host (spec 06-provisioning.md 6.5).
func (e *Executor) execAnsible(ctx context.Context, step *config.AnsibleStep, env Env) error {
	if err := e.checkPlaybook(ctx, env.ProjectRoot); err != nil {
		return err
	}
	// Checked here rather than up front when a galaxy step may have installed
	// it in the meantime.
	if err := e.checkPlugin(ctx, env.ProjectRoot); err != nil {
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
	varsPath := filepath.Join(dir, "idev-vars.json")
	if err := writeJSON(varsPath, env.AnsibleVars()); err != nil {
		return fmt.Errorf("write idev vars: %w", err)
	}

	// Secrets go in a file of their own: a mode 0600 temporary file, deleted
	// once the run is over.
	secretsPath := filepath.Join(dir, "secrets.yml")
	if len(env.Secrets) > 0 {
		if err := writeSecrets(secretsPath, env.Secrets); err != nil {
			return fmt.Errorf("write secrets: %w", err)
		}
	}

	args := runner.Args("-i", inventoryPath)
	if step.Inventory != "" {
		args.Add("-i", resolve(env.ProjectRoot, step.Inventory))
	}
	// Pass idev's variables first, so the project can override them.
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

// inventoryFor builds the contents of the temporary inventory idev generates.
func inventoryFor(env Env) map[string]any {
	host := map[string]any{
		"ansible_host":       env.Instance,
		"ansible_connection": ConnectionPlugin,
		// The local Incus is the only one operated on (spec 05-incus.md 5.7.1).
		"ansible_incus_remote":  "local",
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

// writeSecrets writes the secrets as YAML that Ansible will not template.
//
// Everything --extra-vars reads is a template, so a secret holding {{ was
// evaluated rather than delivered: "abc{{ 1 + 1 }}def" arrived as "abc2def",
// and one naming an undefined variable aborted the play with an error about a
// name the user never wrote. The value may never be logged, which leaves
// almost nothing to diagnose it by. !unsafe is Ansible's own way of saying a
// scalar is data.
//
// The values are written as JSON strings, which are valid YAML double-quoted
// scalars, so quotes, newlines and non-ASCII survive without a YAML library
// deciding how to fold them.
func writeSecrets(path string, secrets map[string]string) error {
	var sb strings.Builder
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		// A string always marshals, and JSON's escaping is exactly what a
		// YAML double-quoted scalar accepts.
		quoted, _ := json.Marshal(secrets[name]) //nolint:errchkjson // a string cannot fail
		sb.WriteString(name + ": !unsafe " + string(quoted) + "\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

// ansibleEnv points Ansible at the project's ansible.cfg when it has one
// (spec 6.5.3).
func ansibleEnv(root string) []string {
	cfg := filepath.Join(root, config.ConfigDir, "ansible", "ansible.cfg")
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

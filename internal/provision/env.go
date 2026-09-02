// Package provision runs the bootstrap and provision steps.
//
// devkit holds no opinion on what to run. It runs the steps declared in
// dev.yml, in the order they are declared (spec 06-provisioning.md).
package provision

// Env is the run-time context devkit passes to every step.
type Env struct {
	ProjectName     string
	ProjectRoot     string // on the host
	Instance        string
	Workspace       string // inside the container
	WorkspaceSource string // on the host
	Remote          string
	IncusProject    string
	// Secrets are the values injected from the host. Their values are hidden
	// when displayed (spec 04-cli.md 4.10).
	Secrets map[string]string
}

// EnvVars returns the environment variables given to a run step (spec 3.10.1).
func (e Env) EnvVars() map[string]string {
	return map[string]string{
		"DEVKIT_PROJECT_NAME":     e.ProjectName,
		"DEVKIT_INSTANCE":         e.Instance,
		"DEVKIT_WORKSPACE":        e.Workspace,
		"DEVKIT_WORKSPACE_SOURCE": e.WorkspaceSource,
		"DEVKIT_INCUS_REMOTE":     e.Remote,
		"DEVKIT_INCUS_PROJECT":    e.IncusProject,
	}
}

// AnsibleVars returns the variables given to an ansible step (spec 3.10.2).
func (e Env) AnsibleVars() map[string]any {
	return map[string]any{
		"devkit_project_name":     e.ProjectName,
		"devkit_instance":         e.Instance,
		"devkit_workspace":        e.Workspace,
		"devkit_workspace_source": e.WorkspaceSource,
		"devkit_incus_remote":     e.Remote,
		"devkit_incus_project":    e.IncusProject,
	}
}

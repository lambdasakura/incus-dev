// Package provision runs the bootstrap and provision steps.
//
// idev holds no opinion on what to run. It runs the steps declared in
// dev.yml, in the order they are declared (spec 06-provisioning.md).
package provision

// Env is the run-time context idev passes to every step.
type Env struct {
	ProjectName     string
	ProjectRoot     string // on the host
	Instance        string
	Workspace       string // inside the container
	WorkspaceSource string // on the host
	IncusProject    string
	// Secrets are the values injected from the host. Their values are hidden
	// when displayed (spec 04-cli.md 4.10).
	Secrets map[string]string
}

// EnvVars returns the environment variables given to a run step (spec 3.10.1).
func (e Env) EnvVars() map[string]string {
	return map[string]string{
		"IDEV_PROJECT_NAME":     e.ProjectName,
		"IDEV_INSTANCE":         e.Instance,
		"IDEV_WORKSPACE":        e.Workspace,
		"IDEV_WORKSPACE_SOURCE": e.WorkspaceSource,
		"IDEV_INCUS_PROJECT":    e.IncusProject,
	}
}

// AnsibleVars returns the variables given to an ansible step (spec 3.10.2).
func (e Env) AnsibleVars() map[string]any {
	return map[string]any{
		"idev_project_name":     e.ProjectName,
		"idev_instance":         e.Instance,
		"idev_workspace":        e.Workspace,
		"idev_workspace_source": e.WorkspaceSource,
		"idev_incus_project":    e.IncusProject,
	}
}

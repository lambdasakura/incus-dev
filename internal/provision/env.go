// Package provision は bootstrap と provision ステップの実行を担当する。
//
// devkitは「何を実行するか」を持たない。dev.yml に宣言されたステップを
// 定義された順序で実行するだけである（仕様 06-provisioning.md）。
package provision

// Env はdevkitが各ステップへ渡す実行文脈。
type Env struct {
	ProjectName     string
	ProjectRoot     string // ホスト側
	Instance        string
	Workspace       string // コンテナ内
	WorkspaceSource string // ホスト側
	Remote          string
	IncusProject    string
	// Secrets はホスト側から注入された秘密情報。
	// 表示時は値を隠す（仕様 04-cli.md 4.10）。
	Secrets map[string]string
}

// EnvVars は run ステップへ渡す環境変数を返す（仕様 3.10.1）。
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

// AnsibleVars は ansible ステップへ渡す変数を返す（仕様 3.10.2）。
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

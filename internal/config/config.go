// Package config は .incus-dev/dev.yml の読み込みとvalidationを担当する。
//
// このパッケージはIncus操作もステップ実行も行わない（仕様 07-implementation.md 7.1）。
package config

import "path/filepath"

// SchemaVersion はこのCLIが解釈できる dev.yml のschema version。
const SchemaVersion = 1

// RuntimeVersion はこのCLIが提供するruntimeの互換バージョン。
// dev.yml の runtime.version との互換判定に使用する（仕様 03-configuration.md 3.4）。
const RuntimeVersion = "1.0"

// 既定値（仕様 03-configuration.md 3.6.3 / 3.7）
const (
	DefaultWorkspaceSource = "."
	DefaultWorkspaceTarget = "/workspace"
	DefaultProfile         = "default"
	DefaultInstanceType    = "container"
	DefaultShell           = "/bin/sh"
	DefaultStoragePool     = "default"
)

// ReservedConfigPrefix はdevkitが管理用に予約するinstance configの名前空間。
const ReservedConfigPrefix = "user.incus-devkit."

// WorkspaceDeviceName はworkspace用のdisk deviceに使う予約名。
const WorkspaceDeviceName = "workspace"

// IDMapMode はworkspaceのuid/gid対応付け方式。
type IDMapMode string

const (
	// IDMapAuto は環境に応じて raw / shift を選ぶ（既定）。
	IDMapAuto IDMapMode = "auto"
	// IDMapRaw は raw.idmap でホストのuid/gidをコンテナのrootへ対応付ける。
	// ホスト側で root:<uid>:1 の許可が必要。
	IDMapRaw IDMapMode = "raw"
	// IDMapShift は disk device の shift でidmapped mountを使う。
	// 追加のホスト設定を要さないが、コンテナが作ったファイルはホスト側でrootの所有となる。
	IDMapShift IDMapMode = "shift"
	// IDMapNone は何も設定しない。
	IDMapNone IDMapMode = "none"
)

// Config は dev.yml 全体を表す。
type Config struct {
	Schema    int        `json:"schema"`
	Runtime   *Runtime   `json:"runtime,omitempty"`
	Project   Project    `json:"project"`
	Instance  Instance   `json:"instance"`
	Workspace *Workspace `json:"workspace,omitempty"`
	// Bootstrap は省略(nil)と明示的な空リストを区別する（仕様 3.8）。
	Bootstrap *[]Step `json:"bootstrap,omitempty"`
	Provision []Step  `json:"provision,omitempty"`
	Shell     *Shell  `json:"shell,omitempty"`
	Incus     *Incus  `json:"incus,omitempty"`
	// Volumes はinstanceを作り直しても残るデータ領域。
	Volumes map[string]Volume `json:"volumes,omitempty"`
	// Secrets はホスト側から注入する秘密情報。
	Secrets map[string]Secret `json:"secrets,omitempty"`

	// Root はプロジェクトrootの絶対パス。Load時に設定される。
	Root string `json:"-"`
}

// Runtime は要求するruntime互換バージョン。
type Runtime struct {
	Version string `json:"version"`
}

// Scope はinstance名の区別の仕方。
type Scope string

const (
	// ScopeName はプロジェクト名のみを使う（既定）。
	ScopeName Scope = "name"
	// ScopePath はチェックアウト先のパスで区別する。
	ScopePath Scope = "path"
	// ScopeBranch は現在のGitブランチで区別する。
	ScopeBranch Scope = "branch"
)

// Project はプロジェクト情報。
type Project struct {
	Name string `json:"name"`
	// Scope は同一マシンで複数のチェックアウトを扱う際の区別の仕方。
	// 既定は name（従来どおりプロジェクト名のみ）。
	Scope Scope `json:"scope,omitempty"`
}

// ScopeOrDefault は区別の仕方を返す。
func (p Project) ScopeOrDefault() Scope {
	if p.Scope == "" {
		return ScopeName
	}
	return p.Scope
}

// Instance はIncus instanceの宣言。config / devices はIncusへの素通しである。
type Instance struct {
	Image string `json:"image"`
	Type  string `json:"type,omitempty"`
	// Profiles は省略(nil)と明示的な空リストを区別する（仕様 3.6.3）。
	Profiles *[]string            `json:"profiles,omitempty"`
	Config   StringMap            `json:"config,omitempty"`
	Devices  map[string]StringMap `json:"devices,omitempty"`
}

// TypeOrDefault はinstance種別を返す（既定 container）。
func (i Instance) TypeOrDefault() string {
	if i.Type == "" {
		return DefaultInstanceType
	}
	return i.Type
}

// Volume は永続ボリューム（仕様 03-configuration.md 3.16）。
//
// instanceを作り直しても残るため、ビルドキャッシュや
// データベースの実体などを置く。
type Volume struct {
	// Path はコンテナ内のマウント先。
	Path string `json:"path"`
	// Pool はIncusのstorage pool。既定は default。
	Pool string `json:"pool,omitempty"`
	// Size は容量。省略時はpoolの既定に従う。
	Size string `json:"size,omitempty"`
}

// PoolOrDefault はstorage poolを返す。
func (v Volume) PoolOrDefault() string {
	if v.Pool == "" {
		return DefaultStoragePool
	}
	return v.Pool
}

// Secret はホスト側から注入する値（仕様 03-configuration.md 3.12）。
//
// dev.yml はGitへcommitされる前提のため、値そのものは書かない。
// ホストの環境変数かファイルから取り込む。
type Secret struct {
	// Env はホスト側の環境変数名。
	Env string `json:"env,omitempty"`
	// File はホスト側のファイルパス。内容を値とする（前後の空白は除く）。
	File string `json:"file,omitempty"`
	// Optional が真の場合、取得できなくてもエラーにしない。
	Optional bool `json:"optional,omitempty"`
}

// Source は取得元の説明を返す。
func (s Secret) Source() string {
	if s.Env != "" {
		return "environment variable " + s.Env
	}
	return "file " + s.File
}

// Shell は idev shell / idev exec の既定。
type Shell struct {
	// User は実行ユーザー。空ならinstanceの既定（root）。
	User string `json:"user,omitempty"`
	// Command は起動するシェル。
	Command string `json:"command,omitempty"`
	// Cwd は作業ディレクトリ。空なら workspace.target。
	Cwd string `json:"cwd,omitempty"`
}

// Incus はIncus側の操作対象。
type Incus struct {
	// Project はIncus project名。空ならCLIの指定（既定 default）。
	Project string `json:"project,omitempty"`
}

// ShellOrDefault は既定値を補ったshell設定を返す。
func (c *Config) ShellOrDefault() Shell {
	sh := Shell{Command: DefaultShell, Cwd: c.WorkspaceOrDefault().Target}
	if c.Shell == nil {
		return sh
	}
	if c.Shell.User != "" {
		sh.User = c.Shell.User
	}
	if c.Shell.Command != "" {
		sh.Command = c.Shell.Command
	}
	if c.Shell.Cwd != "" {
		sh.Cwd = c.Shell.Cwd
	}
	return sh
}

// IncusProject は dev.yml で指定されたIncus projectを返す。未指定なら空。
func (c *Config) IncusProject() string {
	if c.Incus == nil {
		return ""
	}
	return c.Incus.Project
}

// Workspace はworking treeのマウント宣言。
type Workspace struct {
	Source string    `json:"source,omitempty"`
	Target string    `json:"target,omitempty"`
	IDMap  IDMapMode `json:"idmap,omitempty"`
}

// WorkspaceOrDefault は既定値を補ったworkspace宣言を返す。
func (c *Config) WorkspaceOrDefault() Workspace {
	ws := Workspace{
		Source: DefaultWorkspaceSource,
		Target: DefaultWorkspaceTarget,
		IDMap:  IDMapAuto,
	}
	if c.Workspace == nil {
		return ws
	}
	if c.Workspace.Source != "" {
		ws.Source = c.Workspace.Source
	}
	if c.Workspace.Target != "" {
		ws.Target = c.Workspace.Target
	}
	if c.Workspace.IDMap != "" {
		ws.IDMap = c.Workspace.IDMap
	}
	return ws
}

// WorkspaceSourcePath はworkspace sourceの絶対パスを返す。
func (c *Config) WorkspaceSourcePath() string {
	return c.ResolvePath(c.WorkspaceOrDefault().Source)
}

// ResolvePath はproject rootを基準に相対パスを解決する（仕様 3.11）。
func (c *Config) ResolvePath(p string) string {
	if p == "" {
		return c.Root
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Root, p)
}

// ProfileNames は適用するProfile名を返す。
// 省略時は default、明示的な空リストの場合は空を返す（仕様 3.6.3）。
func (c *Config) ProfileNames() []string {
	if c.Instance.Profiles == nil {
		return []string{DefaultProfile}
	}
	names := make([]string, len(*c.Instance.Profiles))
	copy(names, *c.Instance.Profiles)
	return names
}

// HasAnsibleStep は provision に ansible ステップが含まれるかを返す。
// 既定bootstrapの要否判定に使用する（仕様 06-provisioning.md 6.3.2）。
func (c *Config) HasAnsibleStep() bool {
	for _, s := range c.Provision {
		if s.Ansible != nil {
			return true
		}
	}
	return false
}

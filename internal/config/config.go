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

	// Root はプロジェクトrootの絶対パス。Load時に設定される。
	Root string `json:"-"`
}

// Runtime は要求するruntime互換バージョン。
type Runtime struct {
	Version string `json:"version"`
}

// Project はプロジェクト情報。
type Project struct {
	Name string `json:"name"`
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

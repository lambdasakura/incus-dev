// Package config loads and validates .incus-dev/dev.yml.
//
// It neither talks to Incus nor runs steps (spec 07-implementation.md 7.1).
package config

import "path/filepath"

// SchemaVersion is the dev.yml schema version this CLI understands.
const SchemaVersion = 1

// RuntimeVersion is the runtime compatibility version this CLI provides.
// It is checked against runtime.version in dev.yml (spec 03-configuration.md 3.4).
const RuntimeVersion = "1.0"

// Defaults (spec 03-configuration.md 3.6.3 and 3.7).
const (
	DefaultWorkspaceSource = "."
	DefaultWorkspaceTarget = "/workspace"
	DefaultProfile         = "default"
	DefaultShell           = "/bin/sh"
	DefaultStoragePool     = "default"
)

// ReservedConfigPrefix is the instance-config namespace idev reserves for
// its own bookkeeping.
//
// An instance created before the project was renamed carries
// user.incus-devkit.* instead, and is therefore not recognised as managed.
// There is no migration: nothing was ever released under the old name.
const ReservedConfigPrefix = "user.incus-dev."

// ConfigDir is the name of the project's configuration directory. It is
// declared here as well as in internal/project so that the packages reading
// dev.yml do not have to depend on the one that finds it
// (spec 07-implementation.md 7.1).
const ConfigDir = ".incus-dev"

// WorkspaceDeviceName is the reserved name of the workspace disk device.
const WorkspaceDeviceName = "workspace"

// IDMapMode is how uids and gids are mapped for the workspace.
type IDMapMode string

const (
	// IDMapAuto picks raw or shift depending on the host (the default).
	IDMapAuto IDMapMode = "auto"
	// IDMapRaw maps the host uid/gid onto root in the container with
	// raw.idmap. The host must permit it with root:<uid>:1.
	IDMapRaw IDMapMode = "raw"
	// IDMapShift uses an idmapped mount, through the disk device's shift.
	// It needs no extra host setup, but files the container creates are owned
	// by root on the host.
	IDMapShift IDMapMode = "shift"
	// IDMapNone sets nothing up.
	IDMapNone IDMapMode = "none"
)

// Config is the whole of dev.yml.
type Config struct {
	Schema    int        `json:"schema"`
	Runtime   *Runtime   `json:"runtime,omitempty"`
	Project   Project    `json:"project"`
	Instance  Instance   `json:"instance"`
	Workspace *Workspace `json:"workspace,omitempty"`
	// Bootstrap distinguishes omitted (nil) from an explicit empty list
	// (spec 3.8).
	Bootstrap *[]Step `json:"bootstrap,omitempty"`
	Provision []Step  `json:"provision,omitempty"`
	Shell     *Shell  `json:"shell,omitempty"`
	Incus     *Incus  `json:"incus,omitempty"`
	// Volumes hold data that survives a recreated instance.
	Volumes map[string]Volume `json:"volumes,omitempty"`
	// Secrets are values injected from the host.
	Secrets map[string]Secret `json:"secrets,omitempty"`

	// Root is the absolute path of the project root, set by Load.
	Root string `json:"-"`
}

// Runtime is the runtime compatibility version the project requires.
type Runtime struct {
	Version string `json:"version"`
}

// Scope is how instance names are distinguished.
type Scope string

const (
	// ScopeName uses the project name alone (the default).
	ScopeName Scope = "name"
	// ScopePath distinguishes by the path of the checkout.
	ScopePath Scope = "path"
	// ScopeBranch distinguishes by the current Git branch.
	ScopeBranch Scope = "branch"
)

// Project describes the project.
type Project struct {
	Name string `json:"name"`
	// Scope is how several checkouts on one machine are told apart.
	// The default is name: the project name alone, as before.
	Scope Scope `json:"scope,omitempty"`
}

// ScopeOrDefault returns how names are distinguished.
func (p Project) ScopeOrDefault() Scope {
	if p.Scope == "" {
		return ScopeName
	}
	return p.Scope
}

// Instance declares the Incus instance. config and devices pass straight
// through to Incus.
type Instance struct {
	Image string `json:"image"`
	// Profiles distinguishes omitted (nil) from an explicit empty list
	// (spec 3.6.3).
	Profiles *[]string            `json:"profiles,omitempty"`
	Config   StringMap            `json:"config,omitempty"`
	Devices  map[string]StringMap `json:"devices,omitempty"`
}

// Volume is a persistent volume (spec 03-configuration.md 3.13).
//
// It survives a recreated instance, so it suits build caches and database
// files.
type Volume struct {
	// Path is the mount point inside the container.
	Path string `json:"path"`
	// Pool is the Incus storage pool, defaulting to "default".
	Pool string `json:"pool,omitempty"`
	// Size is the capacity. Omitted, the pool's default applies.
	Size string `json:"size,omitempty"`
}

// PoolOrDefault returns the storage pool.
func (v Volume) PoolOrDefault() string {
	if v.Pool == "" {
		return DefaultStoragePool
	}
	return v.Pool
}

// Secret is a value injected from the host (spec 03-configuration.md 3.12).
//
// dev.yml is meant to be committed, so the value itself is never written in
// it; it comes from a host environment variable or file.
type Secret struct {
	// Env is the name of a host environment variable.
	Env string `json:"env,omitempty"`
	// File is a path on the host. Its contents, trimmed, become the value.
	File string `json:"file,omitempty"`
	// Optional stops a missing value from being an error.
	Optional bool `json:"optional,omitempty"`
}

// Source describes where the value comes from.
func (s Secret) Source() string {
	if s.Env != "" {
		return "environment variable " + s.Env
	}
	return "file " + s.File
}

// Shell holds the defaults for idev shell and idev exec.
type Shell struct {
	// User is the user to run as. Empty means the instance default (root).
	User string `json:"user,omitempty"`
	// Command is the shell to start.
	Command string `json:"command,omitempty"`
	// Cwd is the working directory. Empty means workspace.target.
	Cwd string `json:"cwd,omitempty"`
}

// Incus selects what to operate on in Incus.
type Incus struct {
	// Project is the Incus project name. Empty means whatever the CLI says,
	// which defaults to "default".
	Project string `json:"project,omitempty"`
}

// ShellOrDefault returns the shell settings with defaults filled in.
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

// IncusProject returns the Incus project named in dev.yml, or the empty
// string when it names none.
func (c *Config) IncusProject() string {
	if c.Incus == nil {
		return ""
	}
	return c.Incus.Project
}

// Workspace declares how the working tree is mounted.
type Workspace struct {
	Source string    `json:"source,omitempty"`
	Target string    `json:"target,omitempty"`
	IDMap  IDMapMode `json:"idmap,omitempty"`
}

// WorkspaceOrDefault returns the workspace declaration with defaults filled in.
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

// WorkspaceSourcePath returns the absolute path of the workspace source.
func (c *Config) WorkspaceSourcePath() string {
	return c.ResolvePath(c.WorkspaceOrDefault().Source)
}

// ResolvePath resolves a relative path from the project root (spec 3.11).
func (c *Config) ResolvePath(p string) string {
	if p == "" {
		return c.Root
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Root, p)
}

// ProfileNames returns the profiles to apply: "default" when omitted, and
// nothing at all for an explicit empty list (spec 3.6.3).
func (c *Config) ProfileNames() []string {
	if c.Instance.Profiles == nil {
		return []string{DefaultProfile}
	}
	names := make([]string, len(*c.Instance.Profiles))
	copy(names, *c.Instance.Profiles)
	return names
}

// HasAnsibleStep reports whether provision contains an ansible step. It
// decides whether the default bootstrap is needed (spec 06-provisioning.md
// 6.3.2).
func (c *Config) HasAnsibleStep() bool {
	for _, s := range c.Provision {
		if s.Ansible != nil {
			return true
		}
	}
	return false
}

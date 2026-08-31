package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Problem はvalidationで検出した1件の問題。
type Problem struct {
	Path    string
	Message string
}

// ValidationError は検出した問題をまとめて報告する。
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	var sb strings.Builder
	sb.WriteString("invalid configuration:")
	for _, p := range e.Problems {
		sb.WriteString("\n  - ")
		if p.Path != "" {
			sb.WriteString(p.Path)
			sb.WriteString(": ")
		}
		sb.WriteString(p.Message)
	}
	return sb.String()
}

type problems []Problem

func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (p problems) err() error {
	return &ValidationError{Problems: p}
}

// validateSemantics は構造検証を通過した設定に対する意味的な検証を行う。
func validateSemantics(c *Config, raw map[string]any, ps *problems) {
	validateRuntime(c, ps)
	validateSteps(raw, "bootstrap", false, ps)
	validateSteps(raw, "provision", true, ps)
	validateInstance(c, ps)
	validateShell(c, ps)
	validateStepValues(c, ps)
	validateWorkspace(c, ps)

	if c.Root != "" {
		validatePaths(c, ps)
	}
}

func validateRuntime(c *Config, ps *problems) {
	if c.Runtime == nil || c.Runtime.Version == "" {
		return
	}
	ok, err := runtimeCompatible(c.Runtime.Version, RuntimeVersion)
	switch {
	case err != nil:
		ps.add("runtime.version", "%v", err)
	case !ok:
		ps.add("runtime.version",
			"requires runtime %s but this idev provides %s", c.Runtime.Version, RuntimeVersion)
	}
}

// runtimeCompatible は required が current で満たせるかを判定する。
// majorが一致し、かつ required の minor が current 以下であれば互換とする。
func runtimeCompatible(required, current string) (bool, error) {
	rMajor, rMinor, err := parseVersion(required)
	if err != nil {
		return false, fmt.Errorf("invalid version %q: %w", required, err)
	}
	cMajor, cMinor, err := parseVersion(current)
	if err != nil {
		return false, err
	}
	if rMajor != cMajor {
		return false, nil
	}
	return rMinor <= cMinor, nil
}

func parseVersion(v string) (major, minor int, err error) {
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return 0, 0, fmt.Errorf("expected MAJOR[.MINOR[.PATCH]]")
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("expected MAJOR[.MINOR[.PATCH]]")
	}
	if len(parts) > 1 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("expected MAJOR[.MINOR[.PATCH]]")
		}
	}
	if len(parts) > 2 {
		if _, err := strconv.Atoi(parts[2]); err != nil {
			return 0, 0, fmt.Errorf("expected MAJOR[.MINOR[.PATCH]]")
		}
	}
	return major, minor, nil
}

// validateShell は shell 設定を検証する。
func validateShell(c *Config, ps *problems) {
	if c.Shell == nil {
		return
	}
	for _, f := range []struct{ field, value string }{
		{"user", c.Shell.User},
		{"command", c.Shell.Command},
	} {
		if strings.HasPrefix(f.value, "-") {
			ps.add("shell."+f.field, "must not start with %q", "-")
		}
	}
	if c.Shell.Cwd != "" && !filepath.IsAbs(c.Shell.Cwd) {
		ps.add("shell.cwd", "must be an absolute path in the container, got %q", c.Shell.Cwd)
	}
}

// validateStepValues は、コンテナ内でのコマンド実行時に
// オプションとして解釈されうる値を拒否する。
func validateStepValues(c *Config, ps *problems) {
	check := func(steps []Step, kind string) {
		for i, s := range steps {
			if s.Run == nil {
				continue
			}
			for _, f := range []struct{ field, value string }{
				{"user", s.Run.User},
				{"shell", s.Run.Shell},
			} {
				if strings.HasPrefix(f.value, "-") {
					ps.add(fmt.Sprintf("%s[%d].%s", kind, i, f.field),
						"must not start with %q", "-")
				}
			}
		}
	}

	if c.Bootstrap != nil {
		check(*c.Bootstrap, "bootstrap")
	}
	check(c.Provision, "provision")
}

// runOnlyFields は run ステップ専用のフィールド。
var runOnlyFields = []string{"cwd", "env", "shell", "user"}

// validateSteps はステップの形（run/ansibleの排他性など）を生のドキュメントから検証する。
// 位置情報を正確に報告するため、構造体ではなく raw を見る。
func validateSteps(raw map[string]any, key string, allowAnsible bool, ps *problems) {
	list, ok := raw[key].([]any)
	if !ok {
		return
	}
	for i, item := range list {
		path := fmt.Sprintf("%s[%d]", key, i)
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		_, hasRun := m["run"]
		_, hasAnsible := m["ansible"]

		switch {
		case hasRun && hasAnsible:
			ps.add(path, "run and ansible are mutually exclusive; specify only one")
		case !hasRun && !hasAnsible:
			ps.add(path, "must specify either run or ansible")
		}

		if hasAnsible {
			if !allowAnsible {
				ps.add(path, "ansible steps are not allowed in %s; use run steps only", key)
			}
			var extra []string
			for _, f := range runOnlyFields {
				if _, ok := m[f]; ok {
					extra = append(extra, f)
				}
			}
			if len(extra) > 0 {
				ps.add(path, "%s can only be used with run steps", strings.Join(extra, ", "))
			}
		}
	}
}

// profileNamePattern はIncusのProfile名として妥当な形。
var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateInstance(c *Config, ps *problems) {
	// 位置引数としてincusコマンドへ渡す値は、フラグと解釈されうる。
	if strings.HasPrefix(c.Instance.Image, "-") {
		ps.add("instance.image", "must not start with %q", "-")
	}

	if c.Instance.Profiles != nil {
		for i, name := range *c.Instance.Profiles {
			if !profileNamePattern.MatchString(name) {
				ps.add(fmt.Sprintf("instance.profiles[%d]", i),
					"%q is not a valid profile name", name)
			}
		}
	}

	// "-" で始まるキーはincusコマンドのフラグとして解釈されうる。
	// "=" を含むキーは key=value の分割位置がずれる。
	for _, k := range sortedKeys(c.Instance.Config) {
		if strings.HasPrefix(k, "-") {
			ps.add("instance.config."+k, "key must not start with %q", "-")
		}
		if strings.Contains(k, "=") {
			ps.add("instance.config."+k, "key must not contain %q", "=")
		}
	}
	for _, name := range sortedKeys(c.Instance.Devices) {
		if strings.HasPrefix(name, "-") {
			ps.add("instance.devices."+name, "device name must not start with %q", "-")
		}
		for _, k := range sortedKeys(c.Instance.Devices[name]) {
			if strings.HasPrefix(k, "-") {
				ps.add(fmt.Sprintf("instance.devices.%s.%s", name, k),
					"key must not start with %q", "-")
			}
			if strings.Contains(k, "=") {
				ps.add(fmt.Sprintf("instance.devices.%s.%s", name, k),
					"key must not contain %q", "=")
			}
		}
	}

	// profiles: [] はProfileを一切適用しないため、Incusが必要とする
	// root diskをプロジェクト側で宣言しなければならない。
	if c.Instance.Profiles != nil && len(*c.Instance.Profiles) == 0 && !hasRootDisk(c.Instance.Devices) {
		ps.add("instance.devices",
			"a root disk device is required because instance.profiles is empty; "+
				"declare one (type: disk, path: /, pool: <storage pool>) or use a profile that provides it")
	}

	for _, k := range sortedKeys(c.Instance.Config) {
		if strings.HasPrefix(k, ReservedConfigPrefix) {
			ps.add("instance.config."+k, "%s* is reserved for devkit", ReservedConfigPrefix)
		}
	}
	if _, ok := c.Instance.Devices[WorkspaceDeviceName]; ok {
		ps.add("instance.devices."+WorkspaceDeviceName,
			"%q is reserved for the workspace mount; use the workspace section instead", WorkspaceDeviceName)
	}
}

// isVolumeSource は source がホストのパスではなくストレージボリューム名かを返す。
func isVolumeSource(dev StringMap) bool {
	return dev["type"] == "disk" && dev["pool"] != ""
}

// hasRootDisk はコンテナのrootを提供するdiskがあるかを返す。
func hasRootDisk(devices map[string]StringMap) bool {
	for _, dev := range devices {
		if dev["type"] == "disk" && dev["path"] == "/" {
			return true
		}
	}
	return false
}

func validateWorkspace(c *Config, ps *problems) {
	ws := c.WorkspaceOrDefault()
	if !filepath.IsAbs(ws.Target) {
		ps.add("workspace.target", "must be an absolute path in the container, got %q", ws.Target)
	}
}

func validatePaths(c *Config, ps *problems) {
	ws := c.WorkspaceOrDefault()
	src := c.ResolvePath(ws.Source)
	if info, err := os.Stat(src); err != nil {
		ps.add("workspace.source", "%v", err)
	} else if !info.IsDir() {
		ps.add("workspace.source", "%s is not a directory", src)
	}

	for _, name := range sortedKeys(c.Instance.Devices) {
		dev := c.Instance.Devices[name]
		source, ok := dev["source"]
		if !ok || source == "" || filepath.IsAbs(source) {
			// ホスト側の絶対パスは環境依存のため検査しない。
			continue
		}
		if isVolumeSource(dev) {
			// pool を伴うdiskの source はストレージボリューム名であり、パスではない。
			continue
		}
		if _, err := os.Stat(c.ResolvePath(source)); err != nil {
			ps.add(fmt.Sprintf("instance.devices.%s.source", name), "%v", err)
		}
	}

	if c.Bootstrap != nil {
		checkStepPaths(c, *c.Bootstrap, "bootstrap", ps)
	}
	checkStepPaths(c, c.Provision, "provision", ps)
}

func checkStepPaths(c *Config, steps []Step, key string, ps *problems) {
	for i, s := range steps {
		if s.Ansible == nil {
			continue
		}
		path := fmt.Sprintf("%s[%d].ansible", key, i)
		for _, f := range []struct{ field, value string }{
			{"playbook", s.Ansible.Playbook},
			{"vars", s.Ansible.Vars},
			{"inventory", s.Ansible.Inventory},
		} {
			field, value := f.field, f.value
			if value == "" {
				continue
			}
			if _, err := os.Stat(c.ResolvePath(value)); err != nil {
				ps.add(path+"."+field, "%v", err)
			}
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

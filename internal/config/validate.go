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
	"unicode"
	"unicode/utf8"
)

// Problem is one thing validation found.
type Problem struct {
	Path    string
	Message string
}

// ValidationError reports everything validation found, together.
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

// validateSemantics checks the meaning of a configuration that already passed
// the structural checks.
func validateSemantics(c *Config, raw map[string]any, ps *problems) {
	validateRuntime(c, ps)
	validateSteps(raw, "bootstrap", false, ps)
	validateSteps(raw, "provision", true, ps)
	validateInstance(c, ps)
	validateShell(c, ps)
	validateVolumes(c, ps)
	validateSecrets(c, ps)
	validateStepValues(c, ps)
	validateWorkspaceShape(raw, ps)
	validateWorkspace(c, ps)
	validateMountPaths(c, ps)

	if c.Root != "" {
		validatePaths(c, ps)
	}
}

func validateRuntime(c *Config, ps *problems) {
	if c.Runtime == nil || c.Runtime.Version == "" {
		return
	}
	ok, older, err := runtimeCompatible(c.Runtime.Version, RuntimeVersion)
	switch {
	case err != nil:
		ps.add("runtime.version", "%v", err)
	case !ok && older:
		// Which side is behind, since a pin below what idev provides is
		// refused too and "this idev provides 1.0" reads as idev being old.
		ps.add("runtime.version",
			"%s is older than this idev, which provides %s; "+
				"raise the pin, or keep using the idev it was written for",
			c.Runtime.Version, RuntimeVersion)
	case !ok:
		ps.add("runtime.version",
			"%s needs a newer idev than this one, which provides %s",
			c.Runtime.Version, RuntimeVersion)
	}
}

// runtimeCompatible reports whether current satisfies required: the majors
// must match, and required's minor must not exceed current's.
// runtimeCompatible reports whether idev can honour the pinned version, and
// when it cannot, whether the pin is behind idev rather than ahead of it.
func runtimeCompatible(required, current string) (ok, older bool, err error) {
	rMajor, rMinor, err := parseVersion(required)
	if err != nil {
		return false, false, fmt.Errorf("invalid version %q: %w", required, err)
	}
	cMajor, cMinor, err := parseVersion(current)
	if err != nil {
		return false, false, err
	}

	older = rMajor < cMajor || (rMajor == cMajor && rMinor < cMinor)
	if rMajor != cMajor {
		return false, older, nil
	}
	return rMinor <= cMinor, older, nil
}

func parseVersion(v string) (major, minor int, err error) {
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return 0, 0, errVersionFormat
	}

	// Atoi accepts a sign, so "-1" and "+1" would both parse and be reported
	// as a runtime this idev is too old for, rather than as something that is
	// not a version at all.
	nums := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || strings.HasPrefix(part, "+") {
			return 0, 0, errVersionFormat
		}
		nums[i] = n
	}

	major = nums[0]
	if len(nums) > 1 {
		minor = nums[1]
	}
	return major, minor, nil
}

// errVersionFormat is what every malformed runtime.version reports.
var errVersionFormat = fmt.Errorf("expected MAJOR[.MINOR[.PATCH]]")

// validateVolumes checks the persistent volume declarations.
func validateVolumes(c *Config, ps *problems) {
	for _, name := range sortedKeys(c.Volumes) {
		vol := c.Volumes[name]
		path := "volumes." + name

		if !filepath.IsAbs(vol.Path) {
			ps.add(path+".path", "must be an absolute path in the container, got %q", vol.Path)
		}
		if name == WorkspaceDeviceName {
			ps.add(path, "%q is reserved for the workspace mount", WorkspaceDeviceName)
		}
		if strings.Contains(name, listSeparator) || strings.Contains(name, "/") {
			// Both are separators in idev's own record of the volumes it
			// created (user.incus-dev.volumes, "pool/name" joined by commas).
			ps.add(path, "name must not contain %q or %q", listSeparator, "/")
		}
		if _, conflict := c.Instance.Devices[name]; conflict {
			ps.add(path, "conflicts with instance.devices.%s", name)
		}
		if _, conflict := c.Workspace.mount(name); conflict {
			ps.add(path, "conflicts with workspace.%s: one instance cannot have "+
				"two devices of that name", name)
		}
	}
}

// validateSecrets checks the secret declarations.
func validateSecrets(c *Config, ps *problems) {
	for _, name := range sortedKeys(c.Secrets) {
		secret := c.Secrets[name]
		path := "secrets." + name

		switch {
		case secret.Env != "" && secret.File != "":
			ps.add(path, "env and file are mutually exclusive; specify only one")
		case secret.Env == "" && secret.File == "":
			ps.add(path, "must specify either env or file")
		}
		if strings.HasPrefix(name, reservedEnvPrefix) {
			ps.add(path, "%s* is reserved for idev", reservedEnvPrefix)
		}
	}
}

// validateShell checks the shell settings.
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

// isControl reports whether a rune is a control character, which line-oriented
// output cannot carry.
func isControl(r rune) bool { return unicode.IsControl(r) }

// checkAnsibleTags refuses a tag ansible-playbook would not read as a tag.
//
// Each list is joined with a comma and passed as one argv word, so a tag that
// starts with "-" is read as a flag and a tag containing a comma silently
// becomes two. Every comparable field -- run.user, run.shell, shell.user, the
// device keys -- is checked for the first of those, and the user who writes
// this one got an argparse usage dump from inside a provisioning step.
func checkAnsibleTags(path string, step *AnsibleStep, ps *problems) {
	for _, list := range []struct {
		field string
		tags  []string
	}{
		{"tags", step.Tags},
		{"skip_tags", step.SkipTags},
	} {
		for i, tag := range list.tags {
			where := fmt.Sprintf("%s.%s[%d]", path, list.field, i)
			if strings.HasPrefix(tag, "-") {
				ps.add(where, "must not start with %q", "-")
			}
			if strings.Contains(tag, ",") {
				ps.add(where, "must not contain %q, which separates tags", ",")
			}
		}
	}
}

// validateStepValues rejects values that would be taken for options when the
// command runs inside the container.
func validateStepValues(c *Config, ps *problems) {
	check := func(steps []Step, kind string) {
		for i, s := range steps {
			// The name is one field of one line of `idev provision --list`,
			// which a script reads with cut. A tab in it shifts the columns
			// and a newline makes one step into two rows.
			if idx := strings.IndexFunc(s.Name, isControl); idx >= 0 {
				// The rune, not the byte at that index: half of Cc is
				// multi-byte, and s.Name[idx] would print its lead byte --
				// a character the user never wrote.
				found, _ := utf8.DecodeRuneInString(s.Name[idx:])
				ps.add(fmt.Sprintf("%s[%d].name", kind, i),
					"must not contain a control character (found %q)", found)
			}
			if s.Ansible != nil {
				checkAnsibleTags(fmt.Sprintf("%s[%d].ansible", kind, i), s.Ansible, ps)
			}
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
			for _, k := range sortedKeys(s.Run.Env) {
				path := fmt.Sprintf("%s[%d].env.%s", kind, i, k)
				if strings.Contains(k, "=") {
					// The name and the value are one string to Incus, so an
					// "=" here defines a different variable than it looks like.
					ps.add(path, "name must not contain %q", "=")
				}
			}
			if s.Run.Cwd != "" && !filepath.IsAbs(s.Run.Cwd) {
				ps.add(fmt.Sprintf("%s[%d].cwd", kind, i),
					"must be an absolute path in the container, got %q", s.Run.Cwd)
			}
		}
	}

	if c.Bootstrap != nil {
		check(*c.Bootstrap, "bootstrap")
	}
	check(c.Provision, "provision")
}

// runOnlyFields are the fields only a run step may have.
var runOnlyFields = []string{"cwd", "env", "shell", "user"}

// stepKinds are the keys that name a step's kind.
var stepKinds = []string{"run", "ansible", "galaxy"}

// validateSteps checks the shape of the steps — run, ansible and galaxy being
// mutually exclusive, and so on — against the raw document rather than the
// struct, so that positions can be reported exactly.
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
		var kinds []string
		for _, kind := range stepKinds {
			if _, ok := m[kind]; ok {
				kinds = append(kinds, kind)
			}
		}

		switch {
		case len(kinds) > 1:
			ps.add(path, "%s are mutually exclusive; specify only one", strings.Join(kinds, " and "))
		case len(kinds) == 0:
			ps.add(path, "must specify one of: %s", strings.Join(stepKinds, ", "))
		}

		_, hasRun := m["run"]
		if !hasRun && len(kinds) > 0 {
			if !allowAnsible {
				ps.add(path, "only run steps are allowed in %s", key)
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

// reservedEnvPrefix is the prefix of the environment variables idev injects.
const reservedEnvPrefix = "IDEV_"

// listSeparator is what idev joins its recorded key and device lists with
// (user.incus-dev.managed / .devices), so a name holding one would be read
// back as two.
const listSeparator = ","

// checkKeyShape rejects a key idev or Incus cannot carry.
func checkKeyShape(path, key string, ps *problems) {
	if strings.HasPrefix(key, "-") {
		ps.add(path, "key must not start with %q", "-")
	}
	if strings.Contains(key, "=") {
		ps.add(path, "key must not contain %q", "=")
	}
	if strings.Contains(key, listSeparator) {
		ps.add(path, "key must not contain %q", listSeparator)
	}
}

// profileNamePattern is the shape of a valid Incus profile name.
var profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateInstance(c *Config, ps *problems) {
	// An image reference starting with "-" cannot be split into a remote and a
	// name.
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

	// Report key shapes Incus will not accept before we try to apply them. It
	// rejects keys starting with "-" and keys containing "=".
	for _, k := range sortedKeys(c.Instance.Config) {
		checkKeyShape("instance.config."+k, k, ps)
	}
	for _, name := range sortedKeys(c.Instance.Devices) {
		if strings.HasPrefix(name, "-") {
			ps.add("instance.devices."+name, "device name must not start with %q", "-")
		}
		if strings.Contains(name, listSeparator) {
			ps.add("instance.devices."+name, "device name must not contain %q", listSeparator)
		}
		for _, k := range sortedKeys(c.Instance.Devices[name]) {
			checkKeyShape(fmt.Sprintf("instance.devices.%s.%s", name, k), k, ps)
		}
	}

	// profiles: [] applies no profile at all, so the project has to declare
	// the root disk Incus requires.
	if c.Instance.Profiles != nil && len(*c.Instance.Profiles) == 0 && !hasRootDisk(c.Instance.Devices) {
		ps.add("instance.devices",
			"a root disk device is required because instance.profiles is empty; "+
				"declare one (type: disk, path: /, pool: <storage pool>) or use a profile that provides it")
	}

	for _, k := range sortedKeys(c.Instance.Config) {
		if strings.HasPrefix(k, ReservedConfigPrefix) {
			ps.add("instance.config."+k, "%s* is reserved for idev", ReservedConfigPrefix)
		}
	}
	if _, ok := c.Instance.Devices[WorkspaceDeviceName]; ok {
		ps.add("instance.devices."+WorkspaceDeviceName,
			"%q is reserved for the workspace mount; use the workspace section instead", WorkspaceDeviceName)
	}
}

// isVolumeSource reports whether source names a storage volume rather than a
// path on the host.
func isVolumeSource(dev StringMap) bool {
	return dev["type"] == "disk" && dev["pool"] != ""
}

// hasRootDisk reports whether a disk provides the container's root.
//
// The pool counts: Incus refuses a root disk without one ("Root disk entry
// must have a \"pool\" property set"), and the message this guards names all
// three keys. Accepting two of them let a half-declared profiles: [] setup
// through the one check written to catch it.
func hasRootDisk(devices map[string]StringMap) bool {
	for _, dev := range devices {
		if dev["type"] == "disk" && dev["path"] == "/" && dev["pool"] != "" {
			return true
		}
	}
	return false
}

func validateWorkspace(c *Config, ps *problems) {
	// The section holds it in both forms, so one check covers both. It used
	// to be an enum in the schema, which could not reach the map form
	// without reporting every branch that failed.
	if mode := c.WorkspaceOrDefault().IDMap; !slices.Contains(IDMapModes, mode) {
		modes := make([]string, 0, len(IDMapModes))
		for _, m := range IDMapModes {
			modes = append(modes, string(m))
		}
		ps.add("workspace.idmap", "must be one of %s, got %q",
			strings.Join(modes, ", "), mode)
	}

	mounts := c.Mounts()
	validateOwner(c, ps)

	for _, name := range sortedKeys(mounts) {
		mount := mounts[name]
		target := c.mountPath(name, "target")

		switch {
		case mount.Target == "":
			// Only main has a default. Letting the rest share /workspace
			// would have two mounts fight over one directory.
			ps.add(target, "is required: a mount other than %q has no default "+
				"container path", MainMountName)
		case !filepath.IsAbs(mount.Target):
			ps.add(target, "must be an absolute path in the container, got %q", mount.Target)
		case isContainerRoot(mount.Target):
			ps.add(target, "%s", rootIsNotAMountTarget)
		}

		if name == MainMountName {
			continue
		}
		validateMountName(c, name, ps)
	}
}

// validateOwner checks workspace.owner against the mapping mode.
//
// Refused rather than ignored where it cannot take effect: shift maps a
// container id onto the host id of the same number and has no target to
// choose, none maps nothing, and an author who wrote owner believes it applied
// (spec 03-configuration.md 3.7.3).
func validateOwner(c *Config, ps *problems) {
	if c.Workspace == nil || c.Workspace.Owner == nil {
		return
	}
	owner := c.WorkspaceOrDefault().Owner

	if owner.UID < 0 {
		ps.add("workspace.owner.uid", "must not be negative, got %d", owner.UID)
	}
	if owner.GID < 0 {
		ps.add("workspace.owner.gid", "must not be negative, got %d", owner.GID)
	}

	switch mode := c.WorkspaceOrDefault().IDMap; mode {
	case IDMapShift:
		ps.add("workspace.owner", "cannot be used with idmap: shift, which maps "+
			"a container id onto the host id of the same number and has no "+
			"target to choose. Use idmap: raw")
	case IDMapNone:
		ps.add("workspace.owner", "cannot be used with idmap: none, which maps "+
			"nothing. Use idmap: raw")
	}
}

// validateMountName checks a mount name against the device name space it
// joins (spec 3.7.7).
func validateMountName(c *Config, name string, ps *problems) {
	path := "workspace." + name

	switch {
	case name == WorkspaceDeviceName:
		ps.add(path, "%q is the device name %q produces; declare the project's "+
			"own tree as %q instead", WorkspaceDeviceName, MainMountName, MainMountName)
	case strings.HasPrefix(name, "-"):
		ps.add(path, "device name must not start with %q", "-")
	case strings.Contains(name, listSeparator):
		ps.add(path, "device name must not contain %q", listSeparator)
	case len(name) > maxDeviceNameLength:
		// The same cap the schema puts on instance.devices keys. Without it
		// the name reaches Incus and is refused there, after the run has
		// already created volumes.
		ps.add(path, "device name must be at most %d characters, got %d",
			maxDeviceNameLength, len(name))
	}
	if _, conflict := c.Instance.Devices[name]; conflict {
		ps.add(path, "conflicts with instance.devices.%s: one instance cannot "+
			"have two devices of that name", name)
	}
}

// mountPath is where a mount's field was written, which differs between the
// two forms of the workspace section (spec 3.7).
func (c *Config) mountPath(name, field string) string {
	if c.Workspace != nil && c.Workspace.mapForm {
		return "workspace." + name + "." + field
	}
	return "workspace." + field
}

// rootIsNotAMountTarget explains the one absolute path these cannot take.
//
// idev always sets source on the disks it builds, and Incus refuses a root
// disk that has one ("Root disk entry may not have a \"source\" property
// set"), so / passed both offline checks and failed at create.
const rootIsNotAMountTarget = "must not be \"/\": the container's root comes " +
	"from the image or a profile, and a mount cannot replace it"

// isContainerRoot reports whether a container path is the root.
func isContainerRoot(path string) bool {
	return filepath.Clean(path) == "/"
}

// validateMountPaths collects every container path the instance would mount,
// so a second disk on the same one is refused here rather than by Incus after
// the volumes have been created.
//
// The workspace goes first because it is the one entry with a default: a
// volume declared at /workspace is the line the user wrote, so that is the
// line the problem belongs to. Order decides nothing else -- desiredDevices
// merges the same three sources, and a name shared between a volume and a
// device is already refused by validateVolumes.
func validateMountPaths(c *Config, ps *problems) {
	claimed := map[string]string{}
	claim := func(field, path string) {
		if path == "" || !filepath.IsAbs(path) {
			return // reported already, by whoever owns the field
		}
		// Incus compares device paths as written, so two spellings of one
		// path reach it as two disks on the same mount point.
		path = filepath.Clean(path)
		if first, taken := claimed[path]; taken {
			ps.add(field, "container path %s is already used by %s; "+
				"Incus refuses an instance with two disks on one path", path, first)
			return
		}
		claimed[path] = field
	}

	mounts := c.Mounts()
	for _, name := range sortedKeys(mounts) {
		claim(c.mountPath(name, "target"), mounts[name].Target)
	}

	for _, name := range sortedKeys(c.Volumes) {
		path := c.Volumes[name].Path
		if filepath.IsAbs(path) && isContainerRoot(path) {
			ps.add("volumes."+name+".path", "%s", rootIsNotAMountTarget)
			continue
		}
		claim("volumes."+name+".path", path)
	}
	for _, name := range sortedKeys(c.Instance.Devices) {
		// Only a disk mounts a container path; a proxy's path is a socket it
		// forwards, and cannot clash with one.
		if dev := c.Instance.Devices[name]; dev["type"] == "disk" {
			claim("instance.devices."+name+".path", dev["path"])
		}
	}
}

func validatePaths(c *Config, ps *problems) {
	mounts := c.Mounts()
	for _, name := range sortedKeys(mounts) {
		src := c.ResolvePath(mounts[name].Source)
		path := c.mountPath(name, "source")
		if info, err := os.Stat(src); err != nil {
			ps.add(path, "%v", err)
		} else if !info.IsDir() {
			ps.add(path, "%s is not a directory", src)
		}
	}

	for _, name := range sortedKeys(c.Instance.Devices) {
		dev := c.Instance.Devices[name]
		source, ok := dev["source"]
		if !ok || source == "" || filepath.IsAbs(source) {
			// An absolute host path is environment-specific, so leave it alone.
			continue
		}
		if isVolumeSource(dev) {
			// The source of a disk with a pool is a volume name, not a path.
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
		if s.Galaxy != nil {
			path := fmt.Sprintf("%s[%d].galaxy.requirements", key, i)
			if _, err := os.Stat(c.ResolvePath(s.Galaxy.Requirements)); err != nil {
				ps.add(path, "%v", err)
			}
		}
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

// validateWorkspaceShape checks the workspace section against the form it is
// written in (spec 3.7.2).
//
// The schema only says it is an object. Expressing both forms there made every
// mistake report each branch's failure -- "additional properties 'main' not
// allowed" alongside "workspace.main.idmap: false schema" -- which names
// neither the mistake nor what to do about it.
func validateWorkspaceShape(raw map[string]any, ps *problems) {
	section, ok := raw["workspace"].(map[string]any)
	if !ok {
		return
	}

	mapForm := false
	for name, value := range section {
		if _, isObject := value.(map[string]any); isObject && name != ownerKey {
			mapForm = true
			break
		}
	}

	for _, name := range sortedKeys(section) {
		entry, isObject := section[name].(map[string]any)

		if name == ownerKey {
			validateOwnerShape(entry, isObject, ps)
			continue
		}
		if !mapForm {
			if !slices.Contains(singleFormFields, name) {
				ps.add("workspace."+name, "unknown field; the one-mount form takes %s",
					strings.Join(singleFormFields, ", "))
			}
			continue
		}

		if !isObject {
			if name == idmapKey {
				continue
			}
			// The section declares mounts, so a bare source or target has no
			// mount to belong to.
			ps.add("workspace."+name, "belongs to a mount, and workspace declares "+
				"several here; move it into one of them")
			continue
		}
		for _, field := range sortedKeys(entry) {
			switch {
			case field == idmapKey:
				ps.add("workspace."+name+"."+idmapKey,
					"belongs to the instance, not to a mount: raw.idmap is one "+
						"config key and cannot differ per disk. Write it as workspace.idmap")
			case !slices.Contains(mountFields, field):
				ps.add("workspace."+name+"."+field, "unknown field; a mount takes %s",
					strings.Join(mountFields, ", "))
			}
		}
	}
}

// validateOwnerShape checks the section-level owner.
func validateOwnerShape(entry map[string]any, isObject bool, ps *problems) {
	if !isObject {
		ps.add("workspace.owner", "must be a mapping of uid and gid")
		return
	}
	for _, field := range sortedKeys(entry) {
		if !slices.Contains(ownerFields, field) {
			ps.add("workspace.owner."+field, "unknown field; owner takes %s",
				strings.Join(ownerFields, ", "))
		}
	}
}

// singleFormFields are the keys of the one-mount form, and mountFields those
// of one entry in the map form. They differ by idmap, which belongs to the
// section rather than to a mount (spec 3.7.6).
// maxDeviceNameLength is Incus's cap on a device name, which the schema
// carries for instance.devices keys.
const maxDeviceNameLength = 63

var (
	singleFormFields = []string{"idmap", "owner", "readonly", "source", "target"}
	mountFields      = []string{"readonly", "source", "target"}
	ownerFields      = []string{"gid", "uid"}
)

// Package cli defines the commands and orchestrates the Incus operations and
// step execution behind them.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/provision"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// AppOptions configures an App.
type AppOptions struct {
	Config *config.Config
	Client incus.Client
	Runner runner.Runner
	// In is the standard input handed to the shell.
	In io.Reader
	// Out is where results go, such as the output of status.
	Out io.Writer
	// ErrOut is where logs and step output go.
	ErrOut  io.Writer
	Verbose bool
	// Interactive reports whether the standard streams are attached to a
	// terminal. idev shell decides whether to allocate a pseudo-terminal by it.
	Interactive bool
	// Term is the host terminal type (TERM), passed to the container whenever a
	// pseudo-terminal is allocated.
	Term string

	IncusProject string

	// CheckIDMap is the up-front check for workspace.idmap: auto. nil uses the
	// default check.
	CheckIDMap func(uid, gid int) error
	// Host holds the host-side IDs used to map the workspace. nil uses the
	// invoking user's.
	Host *HostIDs
	// Branch returns the current branch name, for project.scope: branch.
	Branch branchFunc
	// LookupEnv reads secrets from host environment variables. nil uses
	// os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// HostIDs are the host-side uid and gid.
type HostIDs struct {
	UID, GID int
}

// App holds what the commands actually do.
type App struct {
	cfg         *config.Config
	client      incus.Client
	exec        *provision.Executor
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	log         *slog.Logger
	instance    string
	interactive bool
	term        string

	incusProject string

	checkIDMap func(uid, gid int) error
	host       HostIDs
	lookupEnv  func(string) (string, bool)
}

// NewApp builds an App.
//
// Use NewAppFor where deriving the instance name can fail, such as
// project.scope: branch without a usable Git.
func NewApp(opt AppOptions) *App {
	app, err := NewAppFor(opt)
	if err != nil {
		// The default scope cannot fail. This is the convenient entry point for
		// tests and default configurations.
		panic(err)
	}
	return app
}

// NewAppFor builds an App, returning an error when the instance name cannot be
// derived.
func NewAppFor(opt AppOptions) (*App, error) {
	out := opt.Out
	if out == nil {
		out = io.Discard
	}
	errOut := opt.ErrOut
	if errOut == nil {
		errOut = io.Discard
	}
	log := newLogger(errOut, opt.Verbose)

	checkIDMap := opt.CheckIDMap
	if checkIDMap == nil {
		checkIDMap = defaultIDMapCheck
	}

	host := opt.Host
	if host == nil {
		host = &HostIDs{UID: os.Getuid(), GID: os.Getgid()}
	}

	name, err := instanceNameFor(opt.Config, opt.Branch)
	if err != nil {
		return nil, err
	}

	lookupEnv := opt.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}

	return &App{
		cfg:    opt.Config,
		client: opt.Client,
		in:     opt.In,
		exec: &provision.Executor{
			Incus:  opt.Client,
			Runner: opt.Runner,
			Logger: log,
			Stdout: errOut,
			Stderr: errOut,
		},
		out:          out,
		errOut:       errOut,
		log:          log,
		interactive:  opt.Interactive,
		term:         opt.Term,
		instance:     name,
		incusProject: opt.IncusProject,
		checkIDMap:   checkIDMap,
		host:         *host,
		lookupEnv:    lookupEnv,
	}, nil
}

// InstanceName returns the instance being operated on.
func (a *App) InstanceName() string { return a.instance }

func (a *App) env() (provision.Env, error) {
	secrets, err := resolveSecrets(a.cfg, a.lookupEnv)
	if err != nil {
		return provision.Env{}, err
	}

	ws := a.cfg.WorkspaceOrDefault()
	return provision.Env{
		ProjectName:     a.cfg.Project.Name,
		ProjectRoot:     a.cfg.Root,
		Instance:        a.instance,
		Workspace:       ws.Target,
		WorkspaceSource: a.cfg.WorkspaceSourcePath(),
		IncusProject:    a.incusProject,
		Secrets:         secrets,
	}, nil
}

// UpOptions controls how idev up behaves.
type UpOptions struct {
	// Restart restarts the instance when a change needs it to take effect.
	Restart bool
}

// Up prepares the instance, then runs bootstrap and provisioning
// (spec 04-cli.md 4.1).
func (a *App) Up(ctx context.Context, opt UpOptions) error {
	a.log.Info("Project: " + a.cfg.Project.Name)

	// Check every host-side prerequisite before creating anything, so a
	// failure part way through does not leave a half-built instance.
	plan, err := a.idmapPlan()
	if err != nil {
		return err
	}
	env, err := a.env()
	if err != nil {
		return err
	}
	if plan.Warning != "" {
		a.log.Warn(plan.Warning)
	}
	if err := a.checkProfiles(ctx); err != nil {
		return err
	}

	created := false

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		if !isManagedBy(inst.Config, a.cfg.Project.Name) {
			return a.unmanagedError(inst)
		}
		a.log.Info("Using existing instance " + a.instance)
		if err := a.reapplyInstance(ctx, inst, plan, opt); err != nil {
			return err
		}
	case errors.Is(err, incus.ErrInstanceNotFound):
		a.log.Info("Creating instance " + a.instance)
		if err := a.ensureVolumes(ctx); err != nil {
			return err
		}
		if err := a.client.CreateInstance(ctx, instanceSpec(a.cfg, a.instance, plan)); err != nil {
			return err
		}
		// The devices were set at creation time, so there is nothing to re-apply.
		created = true
	default:
		return err
	}

	if err := a.ensureVolumes(ctx); err != nil {
		return err
	}

	ws := a.cfg.WorkspaceOrDefault()
	a.log.Info(fmt.Sprintf("Mounting workspace %s -> %s", a.cfg.WorkspaceSourcePath(), ws.Target))
	if !created {
		if err := a.client.ApplyDevices(ctx, a.instance, desiredDevices(a.cfg, plan, a.instance)); err != nil {
			return err
		}
	}

	if err := a.ensureRunning(ctx); err != nil {
		return err
	}
	if err := a.runProvisioning(ctx, env, provision.Selection{}); err != nil {
		return err
	}

	a.log.Info("Development environment is ready")
	return nil
}

// Provision re-runs bootstrap and provisioning without recreating the instance
// (spec 04-cli.md 4.2), or only the part of provisioning sel names.
func (a *App) Provision(ctx context.Context, sel provision.Selection) error {
	// Reject a selection that cannot be resolved, and unmet prerequisites,
	// before touching the instance.
	if _, err := provision.Select(a.cfg.Provision, sel); err != nil {
		return err
	}
	env, err := a.env()
	if err != nil {
		return err
	}

	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if err := a.ensureRunning(ctx); err != nil {
		return err
	}
	return a.runProvisioning(ctx, env, sel)
}

// ListSteps prints the provisioning steps, so you can see the names --step and
// --from accept.
func (a *App) ListSteps() error {
	if len(a.cfg.Provision) == 0 {
		_, err := fmt.Fprintln(a.out, "no provision steps declared")
		return err
	}

	for i, step := range a.cfg.Provision {
		if _, err := fmt.Fprintf(a.out, "%d\t%s\t(%s)\n",
			i+1, step.DisplayName(i+1), stepKind(step)); err != nil {
			return err
		}
	}
	return nil
}

// stepKind returns a step's kind.
func stepKind(step config.Step) string {
	switch {
	case step.Ansible != nil:
		return "ansible"
	case step.Galaxy != nil:
		return "galaxy"
	default:
		return "run"
	}
}

// DestroyOptions controls how idev destroy behaves.
type DestroyOptions struct {
	// Volumes also deletes the persistent volumes.
	Volumes bool
}

// Destroy deletes the instance, leaving the sources on the host alone.
//
// Persistent volumes are kept by default: the point of them is to survive a
// recreated instance, so deleting them is the user's explicit call
// (spec 04-cli.md 4.5).
func (a *App) Destroy(ctx context.Context, opt DestroyOptions) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	a.log.Info("Deleting instance " + a.instance)
	if err := a.client.DeleteInstance(ctx, a.instance); err != nil {
		return err
	}

	if opt.Volumes {
		if err := a.deleteVolumes(ctx); err != nil {
			return err
		}
	} else if len(a.cfg.Volumes) > 0 {
		a.log.Info(fmt.Sprintf("Kept %d volume(s). Use --volumes to delete them", len(a.cfg.Volumes)))
	}

	a.log.Info("Instance deleted. Source tree on the host is untouched")

	return nil
}

// ensureVolumes creates the declared persistent volumes.
func (a *App) ensureVolumes(ctx context.Context) error {
	for _, key := range slices.Sorted(maps.Keys(a.cfg.Volumes)) {
		vol := a.cfg.Volumes[key]
		pool, name := vol.PoolOrDefault(), volumeName(a.instance, key)

		exists, err := a.client.VolumeExists(ctx, pool, name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		a.log.Info(fmt.Sprintf("Creating volume %s on pool %s", name, pool))

		config := map[string]string{}
		if vol.Size != "" {
			config["size"] = vol.Size
		}
		if err := a.client.CreateVolume(ctx, pool, name, config); err != nil {
			return err
		}
	}
	return nil
}

// deleteVolumes deletes the declared persistent volumes.
func (a *App) deleteVolumes(ctx context.Context) error {
	for _, key := range slices.Sorted(maps.Keys(a.cfg.Volumes)) {
		vol := a.cfg.Volumes[key]
		pool, name := vol.PoolOrDefault(), volumeName(a.instance, key)

		exists, err := a.client.VolumeExists(ctx, pool, name)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}

		a.log.Info("Deleting volume " + name)
		if err := a.client.DeleteVolume(ctx, pool, name); err != nil {
			return err
		}
	}
	return nil
}

// Rebuild destroys the instance and creates it again.
func (a *App) Rebuild(ctx context.Context) error {
	_, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		// rebuild keeps the persistent volumes, since surviving a rebuild is
		// exactly what they are for.
		if err := a.Destroy(ctx, DestroyOptions{}); err != nil {
			return err
		}
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}
	return a.Up(ctx, UpOptions{})
}

// Shell runs an interactive shell, or a given command, inside the container.
func (a *App) Shell(ctx context.Context, argv []string) error {
	return a.execInContainer(ctx, argv, a.interactive)
}

// Exec runs a command inside the container without allocating a terminal.
//
// It is meant for scripts and CI, and unlike shell it is never interactive.
func (a *App) Exec(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("exec requires a command; use 'idev shell' for an interactive shell")
	}
	return a.execInContainer(ctx, argv, false)
}

// execInContainer runs a command inside the container.
func (a *App) execInContainer(ctx context.Context, argv []string, tty bool) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if err := a.ensureRunning(ctx); err != nil {
		return err
	}

	sh := a.cfg.ShellOrDefault()
	if len(argv) == 0 {
		argv = []string{sh.Command}
	}
	argv, user := asUser(argv, sh)

	opt := incus.ExecOptions{
		Cwd:  sh.Cwd,
		User: user,
		// Allocating a pseudo-terminal when nothing is attached to one puts
		// carriage returns into the output and breaks pipes and redirects.
		TTY:    tty,
		Term:   a.term,
		Stdin:  a.in,
		Stdout: a.out,
		Stderr: a.errOut,
	}

	code, err := a.client.Exec(ctx, a.instance, argv, opt)
	if err != nil {
		// A command that merely exited non-zero has its exit code propagated;
		// it is not devkit's own error.
		var exitErr *runner.ExitError
		if errors.As(err, &exitErr) {
			return &ExitCodeError{Code: exitErr.ExitCode}
		}
		return err
	}
	if code != 0 {
		return &ExitCodeError{Code: code}
	}
	return nil
}

// asUser returns the argv that runs as the given user, and the user to pass to
// Incus.
//
// The Incus exec API only accepts a uid, so a user name is switched to with su
// inside the container, exactly as a run step does it.
func asUser(argv []string, sh config.Shell) (out []string, user string) {
	if sh.User == "" {
		return argv, ""
	}
	if _, err := strconv.Atoi(sh.User); err == nil {
		return argv, sh.User
	}

	return []string{"su", "-s", sh.Command, sh.User, "-c", strings.Join(argv, " ")}, ""
}

// statusReport is what status prints.
type statusReport struct {
	Project      string            `json:"project"`
	Instance     string            `json:"instance"`
	Status       string            `json:"status"`
	Image        string            `json:"image"`
	Workspace    string            `json:"workspace"`
	Source       string            `json:"workspace_source"`
	Exists       bool              `json:"exists"`
	Managed      bool              `json:"managed"`
	Profiles     []string          `json:"profiles,omitempty"`
	Config       map[string]string `json:"config,omitempty"`
	Devices      []string          `json:"devices,omitempty"`
	Steps        int               `json:"provision_steps"`
	Runtime      string            `json:"runtime,omitempty"`
	IncusProject string            `json:"incus_project"`
}

// Status prints the state of the instance.
func (a *App) Status(ctx context.Context, asJSON bool) error {
	ws := a.cfg.WorkspaceOrDefault()
	report := statusReport{
		Project:      a.cfg.Project.Name,
		Instance:     a.instance,
		Status:       "NOT CREATED",
		Image:        a.cfg.Instance.Image,
		Workspace:    ws.Target,
		Source:       a.cfg.WorkspaceSourcePath(),
		Steps:        len(a.cfg.Provision),
		IncusProject: a.incusProject,
	}
	if a.cfg.Runtime != nil {
		report.Runtime = a.cfg.Runtime.Version
	}

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		report.Exists = true
		report.Status = inst.Status
		report.Managed = isManagedBy(inst.Config, a.cfg.Project.Name)
		report.Profiles = inst.Profiles
		report.Config = limitsOf(inst.Config)
		report.Devices = deviceSummary(inst.Devices)
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}

	if asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return a.printStatus(report)
}

func (a *App) printStatus(r statusReport) error {
	rows := [][2]string{
		{"Project", r.Project},
		{"Instance", r.Instance},
		{"Status", r.Status},
		{"Image", r.Image},
		{"Workspace", r.Source + " -> " + r.Workspace},
	}
	if r.Exists {
		rows = append(rows,
			[2]string{"Profiles", strings.Join(r.Profiles, ", ")},
			[2]string{"Devices", strings.Join(r.Devices, ", ")},
			[2]string{"Managed", yesNo(r.Managed)},
		)
		for _, k := range sortedKeys(r.Config) {
			rows = append(rows, [2]string{k, r.Config[k]})
		}
	}
	rows = append(rows,
		[2]string{"Provision", fmt.Sprintf("%d step(s)", r.Steps)},
		[2]string{"Runtime", r.Runtime},
		[2]string{"Incus", orDefault(r.IncusProject, defaultIncusProject)},
	)

	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		if _, err := fmt.Fprintf(a.out, "%-11s %s\n", row[0]+":", row[1]); err != nil {
			return err
		}
	}
	return nil
}

// Validate reports that the configuration is valid. Loading already checked it.
func (a *App) Validate() error {
	_, err := fmt.Fprintf(a.out, "configuration is valid\nProject:    %s\nInstance:   %s\nProvision:  %d step(s)\n",
		a.cfg.Project.Name, a.instance, len(a.cfg.Provision))
	return err
}

// reapplyInstance re-applies what was declared to an existing instance.
func (a *App) reapplyInstance(ctx context.Context, inst *incus.Instance, plan idmapPlan, opt UpOptions) error {
	desired := desiredConfig(a.cfg, plan)
	// Keep the state from before applying; afterwards the difference is gone.
	before := maps.Clone(inst.Config)

	// Undo the devkit-applied keys and devices that the declaration dropped.
	stale := staleConfigKeys(inst.Config, desired, plan)
	if len(stale) > 0 {
		a.log.Info("Removing config no longer declared: " + strings.Join(stale, ", "))
		if err := a.client.UnsetConfig(ctx, a.instance, stale); err != nil {
			return err
		}
	}
	if devices := staleDevices(inst, desiredDevices(a.cfg, plan, a.instance)); len(devices) > 0 {
		a.log.Info("Removing devices no longer declared: " + strings.Join(devices, ", "))
		if err := a.client.RemoveDevices(ctx, a.instance, devices); err != nil {
			return err
		}
	}

	if err := a.client.ApplyConfig(ctx, a.instance, desired); err != nil {
		return err
	}
	return a.settleRestart(ctx, inst.IsRunning(), before, desired, stale, opt)
}

// settleRestart deals with changes that need a restart (spec 05-incus.md
// 5.4.5).
//
// By default it only warns. Restarting happens only when asked for explicitly,
// so nothing the user was running is stopped unexpectedly.
func (a *App) settleRestart(ctx context.Context, running bool, before, desired map[string]string, unset []string, opt UpOptions) error {
	changed := restartRequiredChanges(running, before, desired, unset)
	if len(changed) == 0 {
		return nil
	}

	if !opt.Restart {
		a.log.Warn(fmt.Sprintf(
			"%s changed but the instance is running; restart it to apply (idev up --restart)",
			strings.Join(changed, ", ")))
		return nil
	}

	a.log.Info("Restarting instance to apply " + strings.Join(changed, ", "))
	if err := a.client.StopInstance(ctx, a.instance); err != nil {
		return err
	}
	return a.client.StartInstance(ctx, a.instance)
}

// restartRequiredKeys are the config keys whose changes need a restart.
//
// limits.* is not among them: in a container both increases and decreases take
// effect while it runs (verified on real hardware; VMs are out of scope).
var restartRequiredKeys = []string{idmapConfigKey, "security.nesting", "security.privileged"}

// restartRequiredChanges returns the changes that need a restart to take
// effect.
//
// Only keys devkit actually changed or unset count. Including untouched keys
// would warn on every run even when nothing happened.
func restartRequiredChanges(running bool, before, desired map[string]string, unset []string) []string {
	if !running {
		return nil
	}

	var changed []string
	for _, k := range restartRequiredKeys {
		if want, declared := desired[k]; declared && before[k] != want {
			changed = append(changed, k)
		}
	}
	for _, k := range unset {
		if slices.Contains(restartRequiredKeys, k) && before[k] != "" {
			changed = append(changed, k)
		}
	}
	slices.Sort(changed)

	return slices.Compact(changed)
}

// idmapPlan resolves the idmap strategy to apply.
func (a *App) idmapPlan() (idmapPlan, error) {
	return resolveIDMap(a.cfg, a.host.UID, a.host.GID, a.checkIDMap)
}

// checkProfiles verifies the named profiles exist. devkit never creates one.
func (a *App) checkProfiles(ctx context.Context) error {
	var missing []string
	for _, name := range a.cfg.ProfileNames() {
		ok, err := a.client.ProfileExists(ctx, name)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("incus profile(s) not found on this host: %s\n"+
		"devkit does not create profiles; create them or remove them from instance.profiles",
		strings.Join(missing, ", "))
}

// managedInstance fetches the instance and confirms devkit manages it.
func (a *App) managedInstance(ctx context.Context) (*incus.Instance, error) {
	inst, err := a.client.Instance(ctx, a.instance)
	if errors.Is(err, incus.ErrInstanceNotFound) {
		return nil, fmt.Errorf("instance %s does not exist; run 'idev up' first", a.instance)
	}
	if err != nil {
		return nil, err
	}
	if !isManagedBy(inst.Config, a.cfg.Project.Name) {
		return nil, a.unmanagedError(inst)
	}
	return inst, nil
}

func (a *App) unmanagedError(inst *incus.Instance) error {
	return fmt.Errorf("instance %s exists but is not managed by devkit for project %q\n"+
		"refusing to touch it; rename the project or remove the instance manually",
		inst.Name, a.cfg.Project.Name)
}

// ensureRunning starts the instance and waits until commands can run.
func (a *App) ensureRunning(ctx context.Context) error {
	inst, err := a.client.Instance(ctx, a.instance)
	if err != nil {
		return err
	}
	if !inst.IsRunning() {
		a.log.Info("Starting instance " + a.instance)
		if err := a.client.StartInstance(ctx, a.instance); err != nil {
			return err
		}
	}
	err = a.client.WaitReady(ctx, a.instance, incus.WaitOptions{})
	if errors.Is(err, incus.ErrNetworkNotReady) {
		// Some configurations never show an address, so do not stop here.
		// Leave it as a clue for when a step that needs the network fails.
		a.log.Warn(err.Error() + "; steps that need network access may fail")
		return nil
	}
	return err
}

// runProvisioning runs bootstrap and then provisioning (spec 06-provisioning.md
// 6.1).
//
// bootstrap is not skipped for a partial run: it is what makes the provisioner
// work at all, and it is assumed to be cheap and idempotent.
func (a *App) runProvisioning(ctx context.Context, env provision.Env, sel provision.Selection) error {
	if err := a.exec.Bootstrap(ctx, a.cfg, env); err != nil {
		return err
	}
	return a.exec.Provision(ctx, a.cfg, env, sel)
}

// ExitCodeError propagates an exit code as it is.
type ExitCodeError struct{ Code int }

func (e *ExitCodeError) Error() string { return fmt.Sprintf("exited with code %d", e.Code) }

// deviceSummary renders devices as a list of "name(type)".
func deviceSummary(devices map[string]incus.Device) []string {
	out := make([]string, 0, len(devices))
	for _, name := range slices.Sorted(maps.Keys(devices)) {
		out = append(out, fmt.Sprintf("%s(%s)", name, devices[name].Type()))
	}
	return out
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// limitsOf picks out the config keys worth displaying.
func limitsOf(instanceConfig map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range instanceConfig {
		if strings.HasPrefix(k, "limits.") {
			out[k] = v
		}
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

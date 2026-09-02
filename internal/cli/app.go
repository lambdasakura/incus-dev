// Package cli defines the commands and orchestrates the Incus operations and
// step execution behind them.
package cli

import (
	"bytes"
	"context"
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
	// InstanceNameOptional keeps a name that cannot be derived from being an
	// error. validate and provision --list do not operate on the instance, and
	// project.scope: branch needs a git this host may not have.
	InstanceNameOptional bool
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
	cfg      *config.Config
	client   incus.Client
	exec     *provision.Executor
	in       io.Reader
	out      io.Writer
	errOut   io.Writer
	log      *slog.Logger
	instance string
	// instanceErr says why the instance name could not be derived, for the
	// commands that do not need it.
	instanceErr error
	// carried is the volume record of an instance rebuild is replacing. The
	// record lives on the instance, so it has to survive the one being
	// destroyed.
	carried     []string
	interactive bool
	term        string

	incusProject string

	checkIDMap func(uid, gid int) error
	host       HostIDs
	lookupEnv  func(string) (string, bool)
}

// MustNewApp builds an App and panics if it cannot, which makes it unfit for
// anything a user runs. It is here for tests and for callers whose options
// cannot fail: only deriving the instance name can, and the default scope
// derives it from the directory name.
func MustNewApp(opt AppOptions) *App {
	app, err := NewApp(opt)
	if err != nil {
		panic(err)
	}
	return app
}

// NewApp builds an App, returning an error when the instance name cannot be
// derived -- project.scope: branch without a usable Git, say.
func NewApp(opt AppOptions) (*App, error) {
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

	name, nameErr := instanceNameFor(opt.Config, opt.Branch)
	if nameErr != nil && !opt.InstanceNameOptional {
		return nil, nameErr
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
		instanceErr:  nameErr,
		incusProject: opt.IncusProject,
		checkIDMap:   checkIDMap,
		host:         *host,
		lookupEnv:    lookupEnv,
	}, nil
}

// HasVolumes reports whether 'destroy --volumes' would delete anything, so
// the confirmation escalates exactly when there is data to lose.
//
// The declaration is not enough on its own: destroy deletes every volume the
// instance recorded, including ones dev.yml has since dropped — which is the
// case the flag exists for, and the one where a mild prompt is worst.
func (a *App) HasVolumes(ctx context.Context) bool {
	inst, err := a.client.Instance(ctx, a.instance)
	if err != nil {
		// Nothing to confirm about yet; destroy itself reports why.
		return false
	}
	// The record is not proof: destroy never prunes it, so it outlives a
	// volume someone removed by hand. Ask what destroy would actually find,
	// the same way deleteVolumes does.
	for _, ref := range recordedVolumes(inst, a.cfg, a.instance) {
		pool, name, ok := splitVolume(ref)
		if !ok {
			continue
		}
		if exists, err := a.client.VolumeExists(ctx, pool, name); err == nil && exists {
			return true
		}
	}
	return false
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
	plan, env, err := a.preflight(ctx)
	if err != nil {
		return err
	}
	return a.up(ctx, opt, plan, env)
}

// up does the work, given a preflight that has already run. rebuild checks
// before it destroys, so it must not check again afterwards: the warnings
// would be printed twice and the host read twice.
func (a *App) up(ctx context.Context, opt UpOptions, plan idmapPlan, env provision.Env) error {
	a.log.Info("Project: " + a.cfg.Project.Name)

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		if !isManagedBy(inst.Config, a.cfg.Project.Name) {
			return a.unmanagedError(inst)
		}
		a.log.Info("Using existing instance " + a.instance)
		// What the daemon holds, before adoptCarried and pruneVolumeRecord
		// edit inst.Config in place. The write below is skipped only if it
		// would change nothing, and that question is about the daemon.
		asRead := maps.Clone(inst.Config)
		// rebuild stashed the record because it was deleting the instance
		// that held it, and something has put one back in the meantime.
		// Dropping it here would lose the volumes rebuild promised to keep,
		// and up would go on to report success.
		a.adoptCarried(inst)
		// Only once the instance is known to be idev's: allocating storage
		// for one it does not manage would leave a volume behind that idev
		// then refuses to touch.
		if err := a.ensureVolumes(ctx); err != nil {
			return err
		}
		a.pruneVolumeRecord(ctx, inst)
		a.warnUnrecorded(inst, plan)
		a.warnChanges(inst, remountsHere)
		if err := a.reapplyInstance(ctx, inst, asRead, plan, opt); err != nil {
			return err
		}
	case errors.Is(err, incus.ErrInstanceNotFound):
		a.warnStrandedInstances(ctx)
		a.log.Info("Creating instance " + a.instance)
		// Before the instance, which mounts them.
		if err := a.ensureVolumes(ctx); err != nil {
			return err
		}
		if err := a.client.CreateInstance(ctx, a.instanceSpec(plan)); err != nil {
			return err
		}
		// The carried record went into the creation request, so it is durable
		// again. Holding it after this would have rebuild report it lost when
		// a later step fails, and offer to delete volumes it still names.
		a.carried = nil
	default:
		return err
	}

	ws := a.cfg.WorkspaceOrDefault()
	// reapplyInstance wrote the devices for an instance that already existed,
	// and CreateInstance carried them for one that did not, so by here the
	// mount is in place either way.
	a.log.Info(fmt.Sprintf("Mounting workspace %s -> %s", a.cfg.WorkspaceSourcePath(), ws.Target))

	// nil: the instance may have been created, or restarted, since the
	// reading above.
	if err := a.ensureRunning(ctx, nil); err != nil {
		return err
	}
	if err := a.runProvisioning(ctx, env, provision.Selection{}); err != nil {
		return err
	}

	// A run that warned is not simply ready: the image, the profiles or a
	// pending restart may all be other than what dev.yml declares, and the
	// warnings are far above by now. What each one means is its own business —
	// the closing line only says how many there were to go back to.
	if n := warningCount(a.log); n > 0 {
		a.log.Info(fmt.Sprintf(
			"Development environment is ready, with %d warning(s) above", n))
	} else {
		a.log.Info("Development environment is ready")
	}
	return nil
}

// Provision re-runs bootstrap and provisioning without recreating the instance
// (spec 04-cli.md 4.2), or only the part of provisioning sel names.
func (a *App) Provision(ctx context.Context, sel provision.Selection) error {
	// Reject a selection that cannot be resolved, and unmet prerequisites,
	// before touching the instance.
	selected, err := provision.Select(a.cfg.Provision, sel)
	if err != nil {
		return err
	}
	if err := a.exec.CheckPrerequisites(ctx, a.cfg.Root, stepsAt(a.cfg.Provision, selected)); err != nil {
		return err
	}
	env, err := a.env()
	if err != nil {
		return err
	}

	inst, err := a.managedInstance(ctx, actsOnMountedTree, adviseUp)
	if err != nil {
		return err
	}
	if err := a.ensureRunning(ctx, inst); err != nil {
		return err
	}
	return a.runProvisioning(ctx, env, sel)
}

// ListSteps prints the provisioning steps, so you can see the names --step and
// --from accept.
func (a *App) ListSteps() error {
	if len(a.cfg.Provision) == 0 {
		// Nothing on stdout: a caller counts the rows, and a sentence there
		// is a row. Saying so belongs with the other things idev says.
		a.log.Info("No provision steps declared")
		return nil
	}

	for i, step := range a.cfg.Provision {
		if _, err := fmt.Fprintf(a.out, "%d\t%s\t(%s)\n",
			i+1, step.DisplayName(i+1), step.Kind()); err != nil {
			return err
		}
	}
	return nil
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
	return a.destroy(ctx, opt, false, nil)
}

// destroy deletes the instance. carrying says the volume record is being taken
// to a replacement instance, which changes what is worth telling the user.
//
// inst is the reading the caller already has, or nil to read it here. rebuild
// has one: it read the instance to take the volume record off it, and nothing
// happens between that and this.
func (a *App) destroy(ctx context.Context, opt DestroyOptions, carrying bool,
	inst *incus.Instance,
) error {
	// rebuild recreates the instance mounted from this checkout; a plain
	// destroy takes away an environment the other one is using.
	eff := sharedEnvironment
	if carrying {
		eff = remountsHere
	}
	var err error
	if inst == nil {
		inst, err = a.managedInstance(ctx, eff, adviseNothing)
	} else {
		inst, err = a.checkManaged(inst, eff)
	}
	if err != nil {
		return err
	}
	// Read what the instance recorded before it is gone. It covers volumes
	// that have since left the declaration, which nothing else names.
	volumes := recordedVolumes(inst, a.cfg, a.instance)

	// The instance goes first: Incus refuses to delete a volume while it is
	// attached to one.
	a.log.Info("Deleting instance " + a.instance)
	if err := a.client.DeleteInstance(ctx, a.instance); err != nil {
		if opt.Volumes {
			return volumesUntouched(err, volumes)
		}
		// Only the interrupted wait can leave the instance gone, and with it
		// the record naming the volumes that left the declaration. Every
		// other way DeleteInstance fails leaves it where it was, and telling
		// a user to delete volumes whose record is intact is telling them to
		// lose data for no reason.
		//
		// The failure says which it was. Looking afterwards cannot: the
		// daemon is still deleting when the answer comes back, so the check
		// would say "still there" in precisely the case this is written for.
		if !errors.Is(err, incus.ErrOutcomeUnknown) {
			return err
		}
		return volumesLeftUnnameable(err, a.instance, undeclaredVolumes(a.cfg, a.instance, volumes))
	}

	if opt.Volumes {
		if err := a.deleteVolumes(ctx, volumes); err != nil {
			return err
		}
	} else if len(volumes) > 0 {
		a.log.Info("Kept volume(s) " + strings.Join(volumes, ", "))
		// "any other" is the ones up will not adopt. Naming a declared volume
		// here would hand the user a command that deletes the data this line
		// just promised to keep.
		others := undeclaredVolumes(a.cfg, a.instance, volumes)
		if !carrying && len(others) > 0 {
			// The record naming them went with the instance, so "use
			// --volumes" would be advice that stopped working the moment it
			// was printed.
			a.log.Info("The next 'idev up' adopts the declared ones again; " +
				"remove any other with " + volumeDeleteHint(others))
		}
	}

	a.log.Info("Instance deleted. Source tree on the host is untouched")

	return nil
}

// Rebuild destroys the instance and creates it again.
func (a *App) Rebuild(ctx context.Context) error {
	// Everything up would refuse to start on, found before the instance is
	// destroyed rather than after (spec 03-configuration.md 3.12).
	plan, env, err := a.preflight(ctx)
	if err != nil {
		return err
	}
	// The image too: rebuild is what the image-drift warning tells the user to
	// run, and creating is where it would otherwise first be resolved — after
	// the old instance is already gone.
	if err := a.client.CheckImage(ctx, a.cfg.Instance.Image); err != nil {
		return err
	}

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		// The record of which volumes are idev's lives on the instance, and
		// the instance is about to go. Carry it over, or a volume that has
		// left the declaration becomes unreachable — which is what rebuild is
		// recommended for in the first place.
		a.carried = recordedVolumes(inst, a.cfg, a.instance)

		// rebuild keeps the persistent volumes, since surviving a rebuild is
		// exactly what they are for.
		if err := a.destroy(ctx, DestroyOptions{}, true, inst); err != nil {
			return err
		}
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}

	if err := a.up(ctx, UpOptions{}, plan, env); err != nil {
		// up clears the carried record once the instance holding it exists,
		// so this names volumes only while they really are unnameable. The
		// ordinary rebuild failure -- a provision step -- happens after that
		// and reports nothing extra.
		return a.recordLostWith(err)
	}
	return nil
}

// Shell runs an interactive shell, or a given command, inside the container.
func (a *App) Shell(ctx context.Context, argv []string, opt ShellOptions) error {
	return a.execInContainer(ctx, argv, a.interactive, opt)
}

// Exec runs a command inside the container without allocating a terminal.
//
// It is meant for scripts and CI, and unlike shell it is never interactive.
func (a *App) Exec(ctx context.Context, argv []string, opt ShellOptions) error {
	return a.execInContainer(ctx, argv, false, opt)
}

// ShellOptions is the per-run override of the shell declaration.
type ShellOptions struct {
	// User replaces shell.user for this run. Empty means the declaration
	// stands: a shell that expanded a variable to nothing and a caller who
	// meant "the instance default" arrive here alike, so the second is asked
	// for by naming the user (spec 04-cli.md 4.3).
	User string
}

// execInContainer runs a command inside the container.
func (a *App) execInContainer(ctx context.Context, argv []string, tty bool, opt ShellOptions) error {
	inst, err := a.managedInstance(ctx, actsOnMountedTree, adviseUp)
	if err != nil {
		return err
	}
	if err := a.ensureRunning(ctx, inst); err != nil {
		return err
	}

	sh := a.cfg.ShellOrDefault()
	if opt.User != "" {
		sh.User = opt.User
	}
	if len(argv) == 0 {
		argv = []string{sh.Command}
	}

	var who shellUser
	if sh.User != "" {
		if who, err = a.resolveShellUser(ctx, sh.User); err != nil {
			return err
		}
	}

	exec := incus.ExecOptions{
		Cwd:       sh.Cwd,
		User:      who.UID,
		Group:     who.GID,
		PublicEnv: who.env(),
		// Allocating a pseudo-terminal when nothing is attached to one puts
		// carriage returns into the output and breaks pipes and redirects.
		TTY:    tty,
		Term:   a.term,
		Stdin:  a.in,
		Stdout: a.out,
		Stderr: a.errOut,
	}

	code, err := a.client.Exec(ctx, a.instance, argv, exec)
	if err != nil {
		// A command that merely exited non-zero has its exit code propagated;
		// it is not idev's own error.
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

// shellUser is what the container knows about the user to run as.
type shellUser struct {
	Name, UID, GID, Home, Shell string
}

// env is what a login would have set, which idev has to set itself.
func (u shellUser) env() map[string]string {
	return map[string]string{
		"HOME": u.Home, "USER": u.Name, "LOGNAME": u.Name, "SHELL": u.Shell,
	}
}

// resolveShellUser looks shell.user up inside the container.
//
// The command runs as the user itself rather than under su. su starts it as
// its own child, so it is not the session leader on the pty and cannot take
// the terminal's foreground process group: bash reports "cannot set terminal
// process group" and runs without job control. Incus runs the process as the
// user, which leaves it owning the terminal -- but then everything su used to
// arrange has to be arranged here, which is what this reads.
//
// A name or a uid: getent takes either, and the gid, home and shell are only
// on the passwd entry whichever was written.
func (a *App) resolveShellUser(ctx context.Context, name string) (shellUser, error) {
	var out bytes.Buffer
	code, err := a.client.Exec(ctx, a.instance, []string{"getent", "passwd", name},
		incus.ExecOptions{Stdout: &out, Stderr: io.Discard})

	if err != nil || code != 0 || strings.TrimSpace(out.String()) == "" {
		// A uid with no passwd entry still names a uid to run as. Incus puts
		// it in root's group, which is what idev did before it asked at all.
		if _, numeric := strconv.Atoi(name); numeric == nil {
			return shellUser{Name: name, UID: name}, nil
		}
		if err != nil {
			return shellUser{}, fmt.Errorf("look up shell.user %q in %s: %w", name, a.instance, err)
		}
		return shellUser{}, fmt.Errorf("shell.user %q does not exist in %s; "+
			"create it during provisioning, or give a uid", name, a.instance)
	}

	// name:password:uid:gid:gecos:home:shell
	fields := strings.Split(strings.TrimSpace(out.String()), ":")
	if len(fields) < 7 {
		return shellUser{}, fmt.Errorf("look up shell.user %q in %s: getent returned %q",
			name, a.instance, out.String())
	}
	return shellUser{
		Name: fields[0], UID: fields[2], GID: fields[3],
		Home: fields[5], Shell: fields[6],
	}, nil
}

// Validate reports that the configuration is valid. Loading already checked it.
func (a *App) Validate() error {
	// Only once the instance name is known: the volume name is built from it,
	// so a dev.yml that is fine for one project.name is not for a longer one.
	if a.instanceErr == nil {
		if err := checkVolumeNames(a.cfg, a.instance); err != nil {
			return err
		}
	}

	instance := a.instance
	if a.instanceErr != nil {
		// The declaration is fine; deriving the name needs something this host
		// cannot answer. Say which, rather than failing on a valid dev.yml.
		instance = "(not derived: " + a.instanceErr.Error() + ")"
	}
	_, err := fmt.Fprintf(a.out, "configuration is valid\nProject:    %s\nInstance:   %s\nProvision:  %d step(s)\n",
		a.cfg.Project.Name, instance, len(a.cfg.Provision))
	return err
}

// reapplyInstance re-applies what was declared to an existing instance.
func (a *App) reapplyInstance(ctx context.Context, inst *incus.Instance, asRead map[string]string, plan idmapPlan, opt UpOptions) error {
	desired := desiredConfig(a.cfg, plan, inst.Config, a.instance)

	// One write, judged against the reading all of this was decided from.
	//
	// desired carries the records -- which volumes are idev's, which devices,
	// which keys -- computed from inst several calls ago; another idev that
	// read before this one and wrote after would otherwise erase what it had
	// recorded in between, and no command would name those volumes again.
	//
	// Config and devices go in together because the records describe the
	// devices. Written apart, a removal that failed after the record had
	// already dropped the device would leave it attached and unrecorded, and
	// nothing would remove it: what idev removes is what the record says it
	// has.
	devices := desiredDevices(a.cfg, plan, a.instance)
	stale := staleConfigKeys(inst.Config, desired, plan)
	staleDev := staleDevices(inst, devices)
	if len(stale) > 0 {
		a.log.Info("Removing config no longer declared: " + strings.Join(stale, ", "))
	}
	if len(staleDev) > 0 {
		a.log.Info("Removing devices no longer declared: " + strings.Join(staleDev, ", "))
	}

	change := incus.InstanceChange{
		SetConfig:     desired,
		UnsetConfig:   stale,
		SetDevices:    devices,
		RemoveDevices: staleDev,
	}
	// A write the instance would not notice is still a write. It is judged
	// against the ETag of the reading above, so every unnecessary one is a
	// chance to lose a race with another idev and fail a run that had nothing
	// to do -- and it lands in the daemon's log as a change, which makes the
	// log useless for finding when something did change.
	if settled(asRead, inst.Devices, change) {
		a.log.Debug("instance already matches dev.yml; nothing to write")
	} else if err := a.client.UpdateInstance(ctx, a.instance, change, inst.ETag); err != nil {
		return a.changedUnderfoot(err)
	}
	// The record rebuild was carrying is on the instance now -- written just
	// above, or already there, which is what settled means. Anything that
	// fails after this must not call it lost, or offer to delete the volumes
	// the instance is at that moment recording.
	a.carried = nil

	return a.settleRestart(ctx, inst.IsRunning(), inst.LastUsedAt, asRead, desired, stale, opt)
}

// settled reports whether change would leave the instance as it already is.
//
// config and devices must be what the daemon returned, not what the caller has
// been working on: adoptCarried and pruneVolumeRecord edit inst.Config in
// place, so by the time the change is built the instance value no longer says
// what is stored. Comparing against it would call a real change settled and
// drop the volume record on the floor.
func settled(config map[string]string, devices map[string]incus.Device, change incus.InstanceChange) bool {
	for _, key := range change.UnsetConfig {
		if _, ok := config[key]; ok {
			return false
		}
	}
	for key, value := range change.SetConfig {
		if have, ok := config[key]; !ok || have != value {
			return false
		}
	}
	for _, name := range change.RemoveDevices {
		if _, ok := devices[name]; ok {
			return false
		}
	}
	for name, device := range change.SetDevices {
		if have, ok := devices[name]; !ok || !maps.Equal(have, device) {
			return false
		}
	}
	return true
}

// changedUnderfoot explains a write refused because the instance moved on.
//
// It does not say what was or was not applied. The refused write itself
// changed nothing, but this wraps writes at several points in a run: volumes
// may already exist, the declaration may already be on the instance, and the
// restart writes come after a stop and a start. Running up again is the whole
// of the answer either way, so that is what it says.
func (a *App) changedUnderfoot(err error) error {
	if !errors.Is(err, incus.ErrChanged) {
		return err
	}
	return fmt.Errorf("%w\nsomething else changed %s while this run was working on it, "+
		"most likely another idev; run 'idev up' again once it has finished",
		err, a.instance)
}

// idmapPlan resolves the idmap strategy to apply.
func (a *App) idmapPlan() (idmapPlan, error) {
	return resolveIDMap(a.cfg, a.host.UID, a.host.GID, a.checkIDMap)
}

// warnImageChanged says so when instance.image no longer matches what the
// instance was made from.
//
// up does not re-image an existing instance, so without this the edit simply
// has no effect and nothing says why (spec 05-incus.md 5.4.3).
func (a *App) warnImageChanged(inst *incus.Instance) {
	was := inst.Config[managedImageKey]
	if was == "" || was == a.cfg.Instance.Image {
		return
	}
	a.log.Warn(fmt.Sprintf(
		"instance.image is %s but this instance was created from %s; "+
			"run 'idev rebuild' to recreate it from the declared image",
		a.cfg.Instance.Image, was))
}

// legacyProjectKey is the marker an idev before the rename wrote.
//
// Such an instance is not adopted -- the markers moved, and nothing migrates
// them -- so the only thing idev can usefully do is say it is there.
const legacyProjectKey = "user.incus-devkit.project"

// legacyRootKey is the checkout that idev recorded before the rename. Read
// alongside the current one: without it, an older instance belonging to a
// different checkout looks stranded, and idev advises deleting a live
// environment that is somebody's working tree.
const legacyRootKey = "user.incus-devkit.root"

// warnStrandedInstances says so when this project already has an instance
// under a name idev no longer derives.
//
// The rules that derive a name have changed, and so has the marker prefix. A
// checkout upgraded across either would otherwise get a second, empty
// environment while the provisioned one keeps running: unreachable by every
// idev command, and with volumes nothing can name.
func (a *App) warnStrandedInstances(ctx context.Context) {
	all, err := a.client.ListInstances(ctx)
	if err != nil {
		// Advisory only. Failing up over it would be worse than the warning
		// it is trying to give.
		a.log.Debug("could not look for older instances: " + err.Error())
		return
	}

	for _, inst := range all {
		if inst.Name == a.instance {
			continue
		}
		project, legacy := inst.Config[managedProjectKey], inst.Config[legacyProjectKey]
		if project != a.cfg.Project.Name && legacy != a.cfg.Project.Name {
			continue
		}
		root := inst.Config[managedRootKey]
		if root == "" {
			root = inst.Config[legacyRootKey]
		}
		if root != "" && root != a.cfg.Root {
			// Another checkout of the same project, which is what
			// project.scope is for. Not stranded.
			continue
		}

		if legacy != "" {
			a.log.Warn(fmt.Sprintf(
				"%s belongs to this project but was made by an older idev, under the "+
					"markers it used then.\n"+
					"                idev will not adopt it; take anything you need out of "+
					"it, then 'incus delete %s'", inst.Name, inst.Name))
			continue
		}
		a.log.Warn(fmt.Sprintf(
			"%s belongs to this project but idev no longer derives that name, so a new "+
				"instance is being created.\n"+
				"                The old one keeps running; remove it with 'incus delete %s'",
			inst.Name, inst.Name))
	}
}

// warnUnrecorded names the settings an instance carries that idev will never
// be able to follow.
//
// Before idev recorded what it applied, there was nothing to tell a key from
// an older dev.yml apart from one set by hand -- and removing the second
// would be wrong, so both are left. This run writes the records, and from
// then on those settings sit outside them for good, so this is the last
// moment anything can point at them.
func (a *App) warnUnrecorded(inst *incus.Instance, plan idmapPlan) {
	if _, ok := inst.Config[managedKeysKey]; ok {
		return
	}
	if _, ok := inst.Config[managedDevicesKey]; ok {
		return
	}

	desiredCfg := desiredConfig(a.cfg, plan, inst.Config, a.instance)
	var loose []string
	for _, k := range slices.Sorted(maps.Keys(inst.Config)) {
		// Incus writes volatile.* and image.* itself. Listing what the user
		// cannot have set, and cannot act on, buries the ones they can.
		if strings.HasPrefix(k, config.ReservedConfigPrefix) ||
			strings.HasPrefix(k, "volatile.") || strings.HasPrefix(k, "image.") {
			continue
		}
		if _, declared := desiredCfg[k]; !declared {
			loose = append(loose, k)
		}
	}

	desiredDev := desiredDevices(a.cfg, plan, a.instance)
	for _, name := range slices.Sorted(maps.Keys(inst.Devices)) {
		if _, declared := desiredDev[name]; !declared {
			loose = append(loose, name+" (device)")
		}
	}

	if len(loose) == 0 {
		return
	}
	a.log.Warn(fmt.Sprintf(
		"this instance predates idev's record of what it applied, so these are left "+
			"alone and will not be followed: %s\n"+
			"                idev cannot tell one an older dev.yml set from one you set "+
			"by hand; remove any you no longer want, or 'idev rebuild' for a clean one",
		strings.Join(loose, ", ")))
}

// warnChanges says what up cannot apply to an existing instance.
func (a *App) warnChanges(inst *incus.Instance, eff checkoutEffect) {
	a.warnDifferentCheckout(inst, eff)
	a.warnImageChanged(inst)
	a.warnProfilesChanged(inst)
	a.warnVolumesDropped(inst)
}

// checkoutEffect is what the running command does to a workspace another
// checkout is using. The warning has to state the consequence: "acts on that
// tree" is a lie for rebuild, which remounts, and for destroy, which touches
// no tree at all.
type checkoutEffect int

const (
	// actsOnMountedTree covers exec, shell and provision: they run against
	// whatever is mounted, which is the other checkout's tree.
	actsOnMountedTree checkoutEffect = iota
	// remountsHere covers up and rebuild, which repoint the workspace here.
	remountsHere
	// wouldRemountHere is the preview's: up --dry-run changes nothing, so it
	// says what up would do rather than what is happening.
	wouldRemountHere
	// sharedEnvironment covers destroy and snapshot: no tree is touched, but
	// the environment the other checkout works in is.
	sharedEnvironment
)

// warnDifferentCheckout says so when the instance was last used from another
// directory.
//
// user.incus-dev.root is recorded for exactly this (spec 05-incus.md 5.2).
// With the default scope two checkouts of one project share an instance by
// design, and up repoints the workspace at whichever ran last — so the other
// one is quietly building this tree.
func (a *App) warnDifferentCheckout(inst *incus.Instance, eff checkoutEffect) {
	was := inst.Config[managedRootKey]
	if was == "" || was == a.cfg.Root {
		return
	}

	effect := map[checkoutEffect]string{
		actsOnMountedTree: "the workspace stays mounted from there, so this acts on that tree",
		remountsHere:      "the workspace is being remounted from this one",
		wouldRemountHere:  "'idev up' would remount the workspace from this one",
		sharedEnvironment: "this environment is in use from there",
	}[eff]
	a.log.Warn(fmt.Sprintf(
		"this instance was last used from %s; %s.\n"+
			"                Set project.scope to path or branch to give each checkout its own instance",
		was, effect))
}

// warnProfilesChanged says so when instance.profiles no longer matches what
// the instance has.
//
// Profiles are set when the instance is created (spec 05-incus.md 5.4.2) and
// idev does not reassign them: they decide the root disk and the network, and
// idev has no record of which of them it put there, so removing one could take
// away a profile the user attached themselves.
func (a *App) warnProfilesChanged(inst *incus.Instance) {
	want := a.cfg.ProfileNames()
	if slices.Equal(want, inst.Profiles) {
		return
	}
	a.log.Warn(fmt.Sprintf(
		"instance.profiles is [%s] but this instance has [%s]; "+
			"profiles are set when the instance is created, so run 'idev rebuild' to change them",
		strings.Join(want, ", "), strings.Join(inst.Profiles, ", ")))
}

// preflight checks every host-side prerequisite, so a failure part way through
// does not leave a half-built instance — and so rebuild does not destroy one
// over a condition it could have found first.
func (a *App) preflight(ctx context.Context) (idmapPlan, provision.Env, error) {
	// Before anything is created: the name is derivable offline, and up would
	// otherwise reach CreateVolume and fail in Incus's words rather than in
	// the one that says which key to shorten.
	if err := checkVolumeNames(a.cfg, a.instance); err != nil {
		return idmapPlan{}, provision.Env{}, err
	}

	plan, err := a.idmapPlan()
	if err != nil {
		return idmapPlan{}, provision.Env{}, err
	}
	env, err := a.env()
	if err != nil {
		return idmapPlan{}, provision.Env{}, err
	}
	if plan.Warning != "" {
		a.log.Warn(plan.Warning)
	}
	if err := a.checkProfiles(ctx); err != nil {
		return idmapPlan{}, provision.Env{}, err
	}
	if err := a.exec.CheckPrerequisites(ctx, a.cfg.Root, a.cfg.Provision); err != nil {
		return idmapPlan{}, provision.Env{}, err
	}
	return plan, env, nil
}

// instanceSpec builds what to pass when creating the instance, carrying over
// the volume record of an instance being replaced.
func (a *App) instanceSpec(plan idmapPlan) incus.InstanceSpec {
	var current map[string]string
	if len(a.carried) > 0 {
		current = map[string]string{managedVolumesKey: strings.Join(a.carried, ",")}
	}
	return instanceSpec(a.cfg, a.instance, plan, current)
}

// checkProfiles verifies the named profiles exist. idev never creates one.
func (a *App) checkProfiles(ctx context.Context) error {
	declared := a.cfg.ProfileNames()
	if len(declared) == 0 {
		return nil
	}
	// One listing for all of them: an answer per name is the same list
	// fetched again and filtered, and two fetches can disagree.
	have, err := a.client.ProfileNames(ctx)
	if err != nil {
		return err
	}

	var missing []string
	for _, name := range declared {
		if !slices.Contains(have, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("incus profile(s) not found on this host: %s\n"+
		"idev does not create profiles; create them or remove them from instance.profiles",
		strings.Join(missing, ", "))
}

// missingAdvice is what to say when the instance is not there.
//
// Every instance command shares this lookup, but not the next step: telling
// someone who is removing an environment to create one is the opposite of
// what they asked for. It says what this run did, not what is left on the
// host -- volumes outlive the instance, so "there is nothing to delete" is a
// claim this lookup cannot make.
type missingAdvice string

const (
	adviseUp      missingAdvice = "run 'idev up' first"
	adviseNothing missingAdvice = "nothing was deleted"
)

// managedInstance fetches the instance and confirms idev manages it.
func (a *App) managedInstance(ctx context.Context, eff checkoutEffect, advice missingAdvice) (*incus.Instance, error) {
	inst, err := a.client.Instance(ctx, a.instance)
	if errors.Is(err, incus.ErrInstanceNotFound) {
		return nil, fmt.Errorf("instance %s does not exist; %s", a.instance, advice)
	}
	if err != nil {
		return nil, err
	}
	return a.checkManaged(inst, eff)
}

// checkManaged confirms idev manages an instance already read.
func (a *App) checkManaged(inst *incus.Instance, eff checkoutEffect) (*incus.Instance, error) {
	if !isManagedBy(inst.Config, a.cfg.Project.Name) {
		return nil, a.unmanagedError(inst)
	}
	// Every command that operates on the instance passes through here, and
	// this is the one that decides whose files are about to be touched: the
	// workspace is a mount of whichever checkout ran up last.
	a.warnDifferentCheckout(inst, eff)

	return inst, nil
}

func (a *App) unmanagedError(inst *incus.Instance) error {
	// project.name may hold dots, underscores and capitals, none of which
	// survive into the instance name, so several names the schema treats as
	// distinct claim one instance. Saying the instance is unmanaged there
	// reads as someone having made it by hand, and sends the user to delete
	// another project's live environment.
	if owner := inst.Config[managedProjectKey]; owner != "" {
		why := "two projects cannot share one instance"
		// Compare the names, not the instances: under project.scope both
		// carry the same suffix, so comparing those would never match and
		// the explanation would be dead exactly where scope is in use.
		if incus.InstanceName(owner) == incus.InstanceName(a.cfg.Project.Name) {
			// The usual cause, and worth naming: nothing in dev.yml hints
			// that a dot or a capital is dropped on the way to the instance.
			why = fmt.Sprintf("%q and %q differ only in characters the instance "+
				"name drops, so both ask for %s", a.cfg.Project.Name, owner, inst.Name)
		}
		return fmt.Errorf("instance %s already belongs to project %q\n%s; rename one of them",
			inst.Name, owner, why)
	}
	return fmt.Errorf("instance %s exists but is not managed by idev for project %q\n"+
		"refusing to touch it; rename the project or remove the instance manually",
		inst.Name, a.cfg.Project.Name)
}

// ensureRunning starts the instance and waits until commands can run.
//
// inst is the reading the caller already has. Pass nil where there is none, or
// where something has happened since -- up creates the instance, and restarts
// it -- and this reads it for itself.
func (a *App) ensureRunning(ctx context.Context, inst *incus.Instance) error {
	if inst == nil {
		var err error
		if inst, err = a.client.Instance(ctx, a.instance); err != nil {
			return err
		}
	}
	if !inst.IsRunning() {
		a.log.Info("Starting instance " + a.instance)
		if err := a.client.StartInstance(ctx, a.instance); err != nil {
			return err
		}
	}
	err := a.client.WaitReady(ctx, a.instance, incus.WaitOptions{})
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

func sortedKeys(m map[string]string) []string {
	return slices.Sorted(maps.Keys(m))
}

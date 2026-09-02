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
	"time"

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
		if err := a.reapplyInstance(ctx, inst, plan, opt); err != nil {
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

	if err := a.ensureRunning(ctx); err != nil {
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

	if _, err := a.managedInstance(ctx, actsOnMountedTree, adviseUp); err != nil {
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
		// Nothing on stdout: a caller counts the rows, and a sentence there
		// is a row. Saying so belongs with the other things idev says.
		a.log.Info("No provision steps declared")
		return nil
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
	return a.destroy(ctx, opt, false)
}

// destroy deletes the instance. carrying says the volume record is being taken
// to a replacement instance, which changes what is worth telling the user.
func (a *App) destroy(ctx context.Context, opt DestroyOptions, carrying bool) error {
	// rebuild recreates the instance mounted from this checkout; a plain
	// destroy takes away an environment the other one is using.
	eff := sharedEnvironment
	if carrying {
		eff = remountsHere
	}
	inst, err := a.managedInstance(ctx, eff, adviseNothing)
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

// volumeDeleteHint renders advice the user can paste. The record joins pool
// and volume with a slash; the command takes them as two operands.
func volumeDeleteHint(volumes []string) string {
	pool, name, ok := splitVolume(volumes[0])
	if !ok {
		return "'incus storage volume delete <pool> <volume>'"
	}
	hint := fmt.Sprintf("'incus storage volume delete %s %s'", pool, name)
	if len(volumes) > 1 {
		hint += ", and so on"
	}
	return hint
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
			// Said out loud: it may be one an earlier instance left behind,
			// and idev is about to record it as its own to remove.
			a.log.Info(fmt.Sprintf("Using existing volume %s on pool %s", name, pool))
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

// deleteVolumes deletes the given volumes, named as pool/name.
//
// The list comes from what the instance recorded, not from the declaration, so
// a volume dropped from dev.yml is still reachable.
func (a *App) deleteVolumes(ctx context.Context, refs []string) error {
	for i, ref := range refs {
		pool, name, ok := splitVolume(ref)
		if !ok {
			continue
		}

		exists, err := a.client.VolumeExists(ctx, pool, name)
		switch {
		case errors.Is(err, incus.ErrPoolNotFound):
			// No volume can be on a pool that is not there. Failing here
			// cannot be retried: the instance, and the record naming the
			// rest, are already gone.
			a.log.Debug("skipping " + ref + ": " + err.Error())
			continue
		case err != nil:
			return remainingVolumes(err, refs[i:])
		}
		if !exists {
			continue
		}

		a.log.Info("Deleting volume " + name)
		if err := a.client.DeleteVolume(ctx, pool, name); err != nil {
			return remainingVolumes(err, refs[i:])
		}
	}
	return nil
}

// namedVolumes keeps the record entries that name a volume.
//
// The record can hold an entry that does not -- it is hand-editable -- and
// sending the user after one wastes their time.
func namedVolumes(refs []string) []string {
	var out []string
	for _, ref := range refs {
		if _, _, ok := splitVolume(ref); ok {
			out = append(out, ref)
		}
	}
	return out
}

// remainingVolumes names what a failed cleanup left behind, for the caller
// that knows the instance is already deleted.
//
// The instance carried the only record naming them, so an error that does not
// list them leaves storage no idev command can name again.
func remainingVolumes(err error, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nthe instance is gone, and with it the record naming "+
		"these volume(s), which were not deleted: %s\nremove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// volumesUntouched names the volumes when deleting the instance failed, so it
// is not known whether the instance is still there.
//
// Only the wait for the operation is ambiguous: a lookup, a force-stop or a
// rejected request all leave the instance -- and its volumes, still attached
// to it -- exactly where they were. Deleting a volume by hand is what Incus
// refuses in that case, so the first thing to try is the same command again.
func volumesUntouched(err error, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nno volume was deleted: %s\n"+
		"run 'idev destroy --volumes' again; if the instance is already gone, "+
		"remove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// volumesLeftUnnameable names the volumes that only the instance's own record
// knew about, for a destroy whose wait was abandoned while the daemon went on
// deleting.
//
// Said only for that one failure -- the others leave the instance, its record
// and its volumes where they were. It does not claim the instance is gone,
// because nothing here knows: that is the whole content of
// incus.ErrOutcomeUnknown. So it gives the user the step that settles it
// before the one that deletes anything.
func volumesLeftUnnameable(err error, instance string, refs []string) error {
	named := namedVolumes(refs)
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nif the instance did go, nothing names these again: %s\n"+
		"check with 'incus list %s', then remove them with %s",
		err, strings.Join(named, ", "), instance, volumeDeleteHint(named))
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
		if err := a.destroy(ctx, DestroyOptions{}, true); err != nil {
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

// adoptCarried folds a record rebuild is carrying into the instance it found.
//
// Rebuild holds the record in memory while the instance that held it is gone.
// Reaching an instance that exists gives it somewhere to live again -- but it
// is still only in memory until the write lands, so the caller stops carrying
// it after that and not before.
func (a *App) adoptCarried(inst *incus.Instance) {
	if len(a.carried) == 0 {
		return
	}
	// Config is never nil here: the caller reached this by finding idev's own
	// project marker in it.
	merged := knownVolumes(inst.Config, a.cfg, a.instance)
	for _, ref := range a.carried {
		if !slices.Contains(merged, ref) {
			merged = append(merged, ref)
		}
	}
	slices.Sort(merged)
	inst.Config[managedVolumesKey] = strings.Join(merged, ",")
}

// recordLostWith names the carried volumes when the create half of a rebuild
// failed, because the record naming them went with the old instance.
//
// The declared ones the next up adopts by name; the rest are reachable only
// through this message.
func (a *App) recordLostWith(err error) error {
	named := namedVolumes(undeclaredVolumes(a.cfg, a.instance, a.carried))
	if len(named) == 0 {
		return err
	}
	return fmt.Errorf("%w\nthe instance holding the volume record is already gone, "+
		"so nothing names these again: %s\n"+
		"keep them for the next 'idev up' by hand, or remove them with %s",
		err, strings.Join(named, ", "), volumeDeleteHint(named))
}

// Shell runs an interactive shell, or a given command, inside the container.
func (a *App) Shell(ctx context.Context, argv []string) error {
	return a.execInContainer(ctx, argv, a.interactive)
}

// Exec runs a command inside the container without allocating a terminal.
//
// It is meant for scripts and CI, and unlike shell it is never interactive.
func (a *App) Exec(ctx context.Context, argv []string) error {
	return a.execInContainer(ctx, argv, false)
}

// execInContainer runs a command inside the container.
func (a *App) execInContainer(ctx context.Context, argv []string, tty bool) error {
	if _, err := a.managedInstance(ctx, actsOnMountedTree, adviseUp); err != nil {
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

	return []string{"su", "-s", sh.Command, sh.User, "-c", shellCommand(argv)}, ""
}

// shellCommand renders an argv as one string for a shell to run.
//
// su -c takes a single string, so the words have to be quoted rather than
// joined: an argument holding a space or a shell metacharacter would
// otherwise be re-split inside the container and run as something else.
func shellCommand(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// statusReport is what status prints.
type statusReport struct {
	Project  string `json:"project"`
	Instance string `json:"instance"`
	Status   string `json:"status"`
	Image    string `json:"image"`
	// ImageDeclared is what dev.yml asks for, set only when the instance was
	// made from something else.
	ImageDeclared string `json:"image_declared,omitempty"`
	Workspace     string `json:"workspace"`
	Source        string `json:"workspace_source"`
	// SourceDeclared is what dev.yml points at, set only when another
	// checkout's tree is the one actually mounted.
	SourceDeclared string            `json:"workspace_source_declared,omitempty"`
	Exists         bool              `json:"exists"`
	Managed        bool              `json:"managed"`
	Profiles       []string          `json:"profiles,omitempty"`
	Config         map[string]string `json:"config,omitempty"`
	Devices        []string          `json:"devices,omitempty"`
	Steps          int               `json:"provision_steps"`
	Runtime        string            `json:"runtime,omitempty"`
	IncusProject   string            `json:"incus_project"`
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
		// What is actually mounted, which is another checkout's tree when one
		// ran up more recently (spec 04-cli.md 4.4).
		if src := inst.Devices[config.WorkspaceDeviceName]["source"]; src != "" {
			report.Source = src
			if src != a.cfg.WorkspaceSourcePath() {
				report.SourceDeclared = a.cfg.WorkspaceSourcePath()
			}
		}
		// What the instance was made from, which up does not change. The
		// declaration is shown beside it when the two disagree, so the row
		// never quietly means one thing or the other.
		if was := inst.Config[managedImageKey]; was != "" {
			report.Image = was
			if was != a.cfg.Instance.Image {
				report.ImageDeclared = a.cfg.Instance.Image
			}
		} else {
			// An instance made before the record. Showing the declaration as
			// the instance's image would say it was made from it, which is
			// the one thing this row is for and the one thing idev cannot
			// know; imageRow says so instead.
			report.Image = ""
			report.ImageDeclared = a.cfg.Instance.Image
		}
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
		{"Image", imageRow(r)},
		{"Workspace", workspaceRow(r)},
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
func (a *App) reapplyInstance(ctx context.Context, inst *incus.Instance, plan idmapPlan, opt UpOptions) error {
	desired := desiredConfig(a.cfg, plan, inst.Config, a.instance)
	// Keep the state from before applying; afterwards the difference is gone.
	before := maps.Clone(inst.Config)

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
	if err := a.client.UpdateInstance(ctx, a.instance, change, inst.ETag); err != nil {
		return a.changedUnderfoot(err)
	}
	// The record rebuild was carrying is on the instance now. Anything that
	// fails after this must not call it lost, or offer to delete the volumes
	// the instance is at that moment recording.
	a.carried = nil

	return a.settleRestart(ctx, inst.IsRunning(), inst.LastUsedAt, before, desired, stale, opt)
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

// settleRestart deals with changes that need a restart (spec 05-incus.md
// 5.4.5).
//
// By default it only warns. Restarting happens only when asked for explicitly,
// so nothing the user was running is stopped unexpectedly.
func (a *App) settleRestart(ctx context.Context, running bool, lastStart time.Time, before, desired map[string]string, unset []string, opt UpOptions) error {
	if !running {
		// It is about to be started, which applies everything. Nothing is
		// owed, including anything an earlier run left owed.
		return a.clearRestartPending(ctx, before)
	}

	fresh, changed, booted := restartOwed(running, before, desired, unset, lastStart)

	if len(changed) == 0 {
		// Nothing owed. A record left by an earlier run was retired by a
		// restart idev did not do, so it goes.
		return a.clearRestartPending(ctx, before)
	}

	if !opt.Restart {
		a.log.Warn(restartWarning(fresh, changed))

		record := recordRestart(lastStart, booted)
		if before[managedRestartKey] == record {
			return nil
		}
		return a.changedUnderfoot(a.client.UpdateInstance(ctx, a.instance,
			incus.InstanceChange{SetConfig: map[string]string{managedRestartKey: record}}, ""))
	}

	a.log.Info("Restarting instance to apply " + strings.Join(changed, ", "))
	if err := a.client.StopInstance(ctx, a.instance); err != nil {
		return err
	}
	if err := a.client.StartInstance(ctx, a.instance); err != nil {
		return err
	}
	return a.clearRestartPending(ctx, before)
}

// pendingRestart returns the keys an earlier run left waiting on a restart,
// and nothing has restarted since.
//
// The record carries the instance's start time as it was when the change was
// applied. A later start — by the user, or by the host coming back up —
// applied it, so there is nothing left to warn about.
// bootedValue is what the running container has for a pending key.
//
// known is false for a record written before the value was stored: such a key
// is owed a restart, and nothing about the declaration can retire it, because
// there is nothing to compare against.
type bootedValue struct {
	value string
	known bool
}

func pendingRestart(before map[string]string, lastStart time.Time) map[string]bootedValue {
	at, entries, ok := strings.Cut(before[managedRestartKey], "|")
	if !ok {
		return nil
	}
	recorded, err := time.Parse(time.RFC3339Nano, at)
	if err != nil || lastStart.After(recorded) {
		// A restart since then applied everything, so nothing is owed.
		return nil
	}

	out := map[string]bootedValue{}
	for _, entry := range splitList(entries) {
		// key=value since this format carries the booted value. An entry
		// without one was written by an older idev, which recorded only that
		// a restart was owed.
		key, value, ok := strings.Cut(entry, "=")
		out[key] = bootedValue{value: value, known: ok}
	}
	return out
}

// restartOwed returns what this run changed and needs a restart for, and the
// full set including what an earlier run changed and nothing has restarted
// since.
//
// The preview computes it the same way, so it can say the same thing up will.
func restartOwed(running bool, before, desired map[string]string, unset []string, lastStart time.Time) (fresh, all []string, owedValues map[string]bootedValue) {
	if !running {
		return nil, nil, nil
	}

	// What the running container actually booted with. An earlier run recorded
	// it for the keys it changed; for the rest it is what is stored, since
	// nothing has changed them since the instance started.
	booted := pendingRestart(before, lastStart)

	owed := map[string]bootedValue{}
	for _, k := range restartRequiredKeys {
		was, recorded := booted[k]
		if !recorded {
			was = bootedValue{value: before[k], known: true}
		}

		want, declared := desired[k]
		switch {
		case declared:
		case slices.Contains(unset, k):
			want = ""
		default:
			// Neither declared nor being unset: the stored value stays as it
			// is, and a restart is owed if the container is not running with
			// it -- an earlier run changed it and nothing has restarted since.
			want = before[k]
		}
		if was.known && sameIDMapping(k, want, was.value) {
			// The container is already running with this. A value changed and
			// changed back is not a change, and restarting would apply
			// nothing while killing whatever is running inside.
			continue
		}
		owed[k] = was
		all = append(all, k)
		if !recorded || before[k] != want {
			fresh = append(fresh, k)
		}
	}

	slices.Sort(fresh)
	slices.Sort(all)
	return fresh, all, owed
}

// restartWarning renders what is owed. Two wordings, because "changed" is
// untrue on a run that changed nothing and is only carrying an earlier one
// forward.
func restartWarning(fresh, all []string) string {
	if len(fresh) > 0 {
		return fmt.Sprintf(
			"%s changed but the instance is running; restart it to apply (idev up --restart)",
			strings.Join(all, ", "))
	}
	return fmt.Sprintf("%s is still waiting on a restart (idev up --restart)",
		strings.Join(all, ", "))
}

// sameIDMapping reports whether two values apply the same thing.
//
// Only raw.idmap needs it: idev used to write "both <id> 0" and now writes
// "uid <id> 0" and "gid <id> 0" on separate lines. The kernel mapping is
// identical when the ids are, so demanding a restart to respell it would cost
// every upgraded instance whatever was running inside it.
func sameIDMapping(key, want, have string) bool {
	if want == have {
		return true
	}
	if key != idmapConfigKey {
		return false
	}
	return normalizeIDMap(want) == normalizeIDMap(have)
}

// normalizeIDMap rewrites a raw.idmap into one comparable form: sorted lines,
// with "both" expanded into its uid and gid halves.
func normalizeIDMap(value string) string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			// Not a shape idev writes; compare it as it is.
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
			continue
		}
		if fields[0] == "both" {
			lines = append(lines,
				"uid "+fields[1]+" "+fields[2],
				"gid "+fields[1]+" "+fields[2])
			continue
		}
		lines = append(lines, strings.Join(fields, " "))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// recordRestart renders the record: when the change was applied, and to what.
func recordRestart(lastStart time.Time, booted map[string]bootedValue) string {
	entries := make([]string, 0, len(booted))
	for _, k := range slices.Sorted(maps.Keys(booted)) {
		if b := booted[k]; b.known {
			entries = append(entries, k+"="+b.value)
		} else {
			// Keep it unknown rather than inventing a value that would let a
			// later run decide the restart is no longer owed.
			entries = append(entries, k)
		}
	}
	return lastStart.Format(time.RFC3339Nano) + "|" + strings.Join(entries, ",")
}

// clearRestartPending drops the record that a restart is owed.
func (a *App) clearRestartPending(ctx context.Context, before map[string]string) error {
	if _, ok := before[managedRestartKey]; !ok {
		return nil
	}
	return a.changedUnderfoot(a.client.UpdateInstance(ctx, a.instance,
		incus.InstanceChange{UnsetConfig: []string{managedRestartKey}}, ""))
}

// restartRequiredKeys are the config keys whose changes need a restart.
//
// limits.* is not among them: in a container both increases and decreases take
// effect while it runs (verified on real hardware; VMs are out of scope).
var restartRequiredKeys = []string{idmapConfigKey, "security.nesting", "security.privileged"}

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

// warnRestartNeeded says what up would say about a restart: one warning, on
// the same stream, covering both what this run would change and what an
// earlier one left owed.
func (a *App) warnRestartNeeded(inst *incus.Instance, plan idmapPlan) {
	if !inst.IsRunning() {
		return
	}
	desired := desiredConfig(a.cfg, plan, inst.Config, a.instance)
	fresh, all, _ := restartOwed(true, inst.Config, desired,
		staleConfigKeys(inst.Config, desired, plan), inst.LastUsedAt)
	if len(all) == 0 {
		return
	}
	a.log.Warn(restartWarning(fresh, all))
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

// pruneVolumeRecord drops from the record the volumes that are no longer on
// the pool, and anything not in the pool/name form.
//
// Without it the record only ever grows, so a warning about a volume outlives
// the volume: deleting one by hand would leave up complaining about it for
// good.
func (a *App) pruneVolumeRecord(ctx context.Context, inst *incus.Instance) {
	recorded, ok := inst.Config[managedVolumesKey]
	if !ok {
		return
	}

	var kept []string
	for _, ref := range splitList(recorded) {
		pool, name, ok := splitVolume(ref)
		if !ok {
			continue
		}
		exists, err := a.client.VolumeExists(ctx, pool, name)
		switch {
		case errors.Is(err, incus.ErrPoolNotFound):
			// The pool has no row, so nothing it names can exist. Keeping the
			// record would warn about the volume on every run, for good, and
			// point at a pool Incus says is not there.
			a.log.Debug("dropping " + ref + ": " + err.Error())
			continue
		case err != nil:
			// Unreachable rather than gone. Nothing declared needs this
			// volume, so refusing to run would block up over a record that is
			// only there to be tidied.
			a.log.Debug("could not check " + ref + ": " + err.Error())
			kept = append(kept, ref)
			continue
		}
		if exists {
			kept = append(kept, ref)
		}
	}
	inst.Config[managedVolumesKey] = strings.Join(kept, ",")
}

// warnVolumesDropped says so when a volume idev created has left the
// declaration.
//
// Nothing names it any more, so the data would sit on the pool with no way to
// reach it (spec 03-configuration.md 3.13).
func (a *App) warnVolumesDropped(inst *incus.Instance) {
	declared := declaredVolumes(a.cfg, a.instance)

	var dropped []string
	for _, ref := range splitList(inst.Config[managedVolumesKey]) {
		if !slices.Contains(declared, ref) {
			dropped = append(dropped, ref)
		}
	}
	if len(dropped) == 0 {
		return
	}
	// 'idev destroy --volumes' would reach them, but it takes the instance and
	// every other volume with it, so it is not the advice to give here.
	a.log.Warn(fmt.Sprintf(
		"volume(s) no longer declared, and their data is kept: %s\n"+
			"                declare them again, or remove one with %s",
		strings.Join(dropped, ", "), volumeDeleteHint(dropped)))
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

// workspaceRow shows what is mounted, and the declaration when another
// checkout's tree is the one in the instance.
func workspaceRow(r statusReport) string {
	out := r.Source + " -> " + r.Workspace
	if r.SourceDeclared != "" {
		out += " (dev.yml points at " + r.SourceDeclared + "; idev up remounts it here)"
	}
	return out
}

// imageRow shows the image, and the declaration when it asks for another one.
func imageRow(r statusReport) string {
	switch {
	case r.ImageDeclared == "":
		return r.Image
	case r.Image == "":
		// Made before idev recorded it. The declaration is all there is, and
		// saying so is the difference between a fact and a guess.
		return r.ImageDeclared + " (declared; this instance predates the record, " +
			"so what it was made from is not recorded)"
	}
	return r.Image + " (dev.yml declares " + r.ImageDeclared + "; idev rebuild to recreate)"
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

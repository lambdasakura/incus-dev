package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/lambdasakura/incus-dev/internal/config"
	"github.com/lambdasakura/incus-dev/internal/incus"
	"github.com/lambdasakura/incus-dev/internal/project"
	"github.com/lambdasakura/incus-dev/internal/provision"
	"github.com/lambdasakura/incus-dev/internal/runner"
)

// globalFlags are the flags every command shares.
type globalFlags struct {
	verbose      bool
	directory    string
	incusProject string
}

// defaultIncusProject is the Incus project used when none was named.
const defaultIncusProject = "default"

// errAborted reports that the confirmation was declined.
var errAborted = errors.New("aborted")

// appFactory builds the App a command uses. Tests replace it.
type appFactory func(*globalFlags) (*App, error)

// NewRootCommand builds the root command of idev.
func NewRootCommand(version string) *cobra.Command {
	return newRootCommand(version, newApp, newOfflineApp)
}

// newRootCommand wires the commands to the factories building their App.
//
// offline is for the commands that make no Incus call. They must keep working
// where no Incus is reachable (spec 04-cli.md 4.7), so they must not be given
// a factory that connects.
func newRootCommand(version string, factory, offline appFactory) *cobra.Command {
	g := &globalFlags{}

	root := &cobra.Command{
		Use:   "idev",
		Short: "Manage per-project development environments with Incus",
		Long: "idev prepares an Incus instance as declared in .incus-dev/dev.yml\n" +
			"and runs the provisioning steps declared there.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "print detailed output")
	pf.StringVarP(&g.directory, "directory", "C", "",
		"directory to start looking for .incus-dev/dev.yml in (default: current directory)")
	pf.StringVar(&g.incusProject, "incus-project", "",
		"Incus project (defaults to incus.project in dev.yml, then \"default\")")

	root.AddCommand(
		newUpCommand(g, factory),
		newProvisionCommand(g, factory, offline),
		newShellCommand(g, factory),
		newExecCommand(g, factory),
		newStatusCommand(g, factory),
		newDestroyCommand(g, factory),
		newRebuildCommand(g, factory),
		newValidateCommand(g, offline),
		newSnapshotCommand(g, factory),
	)
	return root
}

// resolveTarget decides which Incus to operate on.
//
// The CLI wins for the Incus project, then dev.yml, then "default".
func resolveTarget(g *globalFlags, cfg *config.Config) incus.Target {
	project := g.incusProject
	if project == "" {
		project = cfg.IncusProject()
	}
	if project == "" {
		project = defaultIncusProject
	}
	return incus.Target{Project: project}
}

// newApp discovers the project, loads the configuration, connects to Incus and
// builds the App.
func newApp(g *globalFlags) (*App, error) {
	return buildApp(g, incus.Connect)
}

// newOfflineApp builds the App without connecting to Incus, for the commands
// that make no Incus call. `idev validate` is expected to run in a CI job where
// no Incus is reachable (spec 04-cli.md 4.7).
func newOfflineApp(g *globalFlags) (*App, error) {
	return buildApp(g, nil)
}

// offlineOptions marks the App as one that does not operate on the instance.
func offlineOptions(connect func(incus.Target) (*incus.API, error)) bool {
	return connect == nil
}

// buildApp discovers the project, loads the configuration and builds the App.
// It connects to Incus when connect is non-nil.
func buildApp(g *globalFlags, connect func(incus.Target) (*incus.API, error)) (*App, error) {
	start := g.directory
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
		start = wd
	}

	proj, err := project.Discover(start)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(proj.ConfigPath)
	if err != nil {
		return nil, err
	}

	target := resolveTarget(g, cfg)

	log := newLogger(os.Stderr, g.verbose)
	if proj.Shadowed != "" {
		log.Warn(fmt.Sprintf(
			"%s is not a regular file, so it was skipped; this run acts on the project at %s",
			proj.Shadowed, proj.Root))
	}
	cmdRunner := runner.NewWithLogger(log)

	// Left nil for the commands that never reach Incus.
	var client incus.Client
	if connect != nil {
		api, err := connect(target)
		if err != nil {
			return nil, err
		}
		// So --verbose can follow what is done to Incus.
		api.Logger = log
		client = api
	}

	return NewAppFor(AppOptions{
		InstanceNameOptional: offlineOptions(connect),
		Config:               cfg,
		Branch:               gitBranch(context.Background(), cmdRunner, proj.Root),
		Client:               client,
		Runner:               cmdRunner,
		In:                   os.Stdin,
		Out:                  os.Stdout,
		ErrOut:               os.Stderr,
		Verbose:              g.verbose,
		Interactive:          isTerminal(os.Stdin) && isTerminal(os.Stdout),
		Term:                 os.Getenv("TERM"),
		IncusProject:         target.Project,
	})
}

// isTerminal reports whether a file is attached to a terminal.
//
// Allocating a pseudo-terminal when it is not puts carriage returns into the
// output, so idev shell decides by this.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func newUpCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var (
		dryRun  bool
		restart bool
	)

	c := &cobra.Command{
		Use:   "up",
		Short: "Create the instance, then run bootstrap and provisioning",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if dryRun {
				return app.Plan(cmd.Context())
			}
			return app.Up(cmd.Context(), UpOptions{Restart: restart})
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false,
		"show the planned operations without changing anything in Incus")
	c.Flags().BoolVar(&restart, "restart", false,
		"restart the instance if a change needs it to take effect")

	c.MarkFlagsMutuallyExclusive("dry-run", "restart")

	return c
}

// newProvisionCommand builds the provision command.
//
// --list only reads dev.yml, so it is built without connecting: it is meant to
// be usable where no Incus is reachable (spec 04-cli.md 4.2).
func newProvisionCommand(g *globalFlags, newApp, offline appFactory) *cobra.Command {
	var (
		only     []string
		from     string
		listOnly bool
	)

	c := &cobra.Command{
		Use:   "provision",
		Short: "Re-run provisioning without recreating the instance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			build := newApp
			if listOnly {
				build = offline
			}
			app, err := build(g)
			if err != nil {
				return err
			}
			if listOnly {
				return app.ListSteps()
			}
			return app.Provision(cmd.Context(), provision.Selection{Only: only, From: from})
		},
	}

	c.Flags().StringArrayVar(&only, "step", nil,
		"run only the given steps (name or number; may be repeated)")
	c.Flags().StringVar(&from, "from", "",
		"run from the given step onwards (name or number)")
	c.Flags().BoolVar(&listOnly, "list", false,
		"list the provisioning steps (does not touch Incus)")

	c.MarkFlagsMutuallyExclusive("step", "from")
	c.MarkFlagsMutuallyExclusive("step", "list")
	c.MarkFlagsMutuallyExclusive("from", "list")

	return c
}

func newShellCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "shell [-- command...]",
		Short: "Open a shell in the container, or run the given command",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Shell(cmd.Context(), args)
		},
	}
	// Keep idev from reading the -lc of idev shell -- bash -lc "..." as its own
	// flag.
	c.Flags().SetInterspersed(false)

	return c
}

func newExecCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "exec -- <command>",
		Short: "Run a command in the container without allocating a terminal",
		// Checked before the App is built, so the missing command is what the
		// user is told about rather than an unreachable Incus.
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("exec requires a command; use 'idev shell' for an interactive shell")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Exec(cmd.Context(), args)
		},
	}
	// Keep idev from reading the -l of idev exec -- ls -l as its own flag.
	c.Flags().SetInterspersed(false)

	return c
}

func newStatusCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the state of the instance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Status(cmd.Context(), asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return c
}

func newDestroyCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var (
		force   bool
		volumes bool
	)
	c := &cobra.Command{
		Use:   "destroy",
		Short: "Delete the instance (the sources on the host are left alone)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			// --force asks nothing, so it must not pay for the lookup that
			// decides how the question is worded.
			if !force {
				// --volumes takes the data with it, and the data is the half a
				// user is not expecting to lose.
				prompt := fmt.Sprintf("Delete instance %s?", app.InstanceName())
				if volumes && app.HasVolumes(cmd.Context()) {
					prompt = fmt.Sprintf(
						"Delete instance %s AND its persistent volumes, with everything in them?",
						app.InstanceName())
				}
				if !confirm(cmd.InOrStdin(), cmd.OutOrStdout(), prompt) {
					return errAborted
				}
			}
			return app.Destroy(cmd.Context(), DestroyOptions{Volumes: volumes})
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "skip the confirmation prompt")
	c.Flags().BoolVar(&volumes, "volumes", false, "delete the persistent volumes as well")

	return c
}

func newRebuildCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "rebuild",
		Short: "Destroy the instance and create it again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("Destroy and recreate instance %s?", app.InstanceName())) {
				return errAborted
			}
			return app.Rebuild(cmd.Context())
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "skip the confirmation prompt")
	return c
}

func newSnapshotCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "snapshot",
		Short: "Manage snapshots of the instance",
	}

	c.AddCommand(&cobra.Command{
		Use:   "create [name]",
		Short: "Create a snapshot (named after the current time if omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			var name string
			if len(args) > 0 {
				name = args[0]
			}
			return app.CreateSnapshot(cmd.Context(), name)
		},
	})

	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the snapshots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.ListSnapshots(cmd.Context())
		},
	})

	var force bool
	restore := &cobra.Command{
		Use:   "restore <name>",
		Short: "Roll the instance back to a snapshot (its current state is lost)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("Roll instance %s back to %s? Its current state will be lost.",
					app.InstanceName(), args[0])) {
				return errAborted
			}
			return app.RestoreSnapshot(cmd.Context(), args[0])
		},
	}
	restore.Flags().BoolVarP(&force, "force", "f", false, "skip the confirmation prompt")
	c.AddCommand(restore)

	var deleteForce bool
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !deleteForce && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("Delete snapshot %s?", args[0])) {
				return errAborted
			}
			return app.DeleteSnapshot(cmd.Context(), args[0])
		},
	}
	del.Flags().BoolVarP(&deleteForce, "force", "f", false, "skip the confirmation prompt")
	c.AddCommand(del)

	return c
}

func newValidateCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate dev.yml (makes no change to Incus at all)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Validate()
		},
	}
}

// confirm asks for confirmation before a destructive operation.
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	_, _ = fmt.Fprintf(out, "%s [y/N]: ", prompt)

	// Even at EOF, whatever was read counts as the answer, so input without a
	// trailing newline works.
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// Execute runs the root command.
func Execute(ctx context.Context, version string, args []string) error {
	root := NewRootCommand(version)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// Report prints the error and returns the process exit code.
//
// A command inside the container that merely exited non-zero has its exit code
// returned without being shown as idev's own error: its output was already
// streamed.
func Report(w io.Writer, err error) int {
	if err == nil {
		return 0
	}

	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}

	_, _ = fmt.Fprintf(w, "[idev] error: %v\n", err)
	return 1
}

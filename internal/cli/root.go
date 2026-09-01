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

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/project"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// globalFlags は全コマンド共通のフラグ。
type globalFlags struct {
	verbose      bool
	directory    string
	incusRemote  string
	incusProject string
}

// defaultIncusProject は指定が無い場合に使うIncus project。
const defaultIncusProject = "default"

// errAborted は確認を拒否したことを示す。
var errAborted = errors.New("aborted")

// appFactory はコマンドが使う App を生成する。テストでは差し替える。
type appFactory func(*globalFlags) (*App, error)

// NewRootCommand は idev のルートコマンドを構成する。
func NewRootCommand(version string) *cobra.Command {
	return newRootCommand(version, newApp)
}

func newRootCommand(version string, factory appFactory) *cobra.Command {
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
	pf.StringVar(&g.incusRemote, "incus-remote", "local", "Incus remote")
	pf.StringVar(&g.incusProject, "incus-project", "",
		"Incus project (defaults to incus.project in dev.yml, then \"default\")")

	root.AddCommand(
		newUpCommand(g, factory),
		newProvisionCommand(g, factory),
		newShellCommand(g, factory),
		newExecCommand(g, factory),
		newStatusCommand(g, factory),
		newDestroyCommand(g, factory),
		newRebuildCommand(g, factory),
		newValidateCommand(g, factory),
		newSnapshotCommand(g, factory),
	)
	return root
}

// resolveTarget は操作対象のIncusを決める。
//
// Incus projectはCLIの指定を優先し、無ければ dev.yml、それも無ければ default。
func resolveTarget(g *globalFlags, cfg *config.Config) incus.Target {
	project := g.incusProject
	if project == "" {
		project = cfg.IncusProject()
	}
	if project == "" {
		project = defaultIncusProject
	}
	return incus.Target{Remote: g.incusRemote, Project: project}
}

// newApp はproject探索と設定読み込みを行い、Appを構成する。
func newApp(g *globalFlags) (*App, error) {
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
	cmdRunner := runner.NewWithLogger(log)

	client, err := incus.Connect(target)
	if err != nil {
		return nil, err
	}
	// --verbose でIncusへの操作を追えるようにする。
	client.Logger = log

	return NewAppFor(AppOptions{
		Config:       cfg,
		Branch:       gitBranch(context.Background(), cmdRunner, proj.Root),
		Client:       client,
		Runner:       cmdRunner,
		In:           os.Stdin,
		Out:          os.Stdout,
		ErrOut:       os.Stderr,
		Verbose:      g.verbose,
		Interactive:  isTerminal(os.Stdin) && isTerminal(os.Stdout),
		Term:         os.Getenv("TERM"),
		Remote:       target.Remote,
		IncusProject: target.Project,
	})
}

// isTerminal はファイルが端末に接続されているかを返す。
//
// 端末でない場合に擬似端末を割り当てると出力へCRが混入するため、
// idev shell の判断に使う。
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

func newProvisionCommand(g *globalFlags, newApp appFactory) *cobra.Command {
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
			app, err := newApp(g)
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
	// idev shell -- bash -lc "..." の -lc を idev のフラグと解釈させない。
	c.Flags().SetInterspersed(false)

	return c
}

func newExecCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "exec -- <command>",
		Short: "Run a command in the container without allocating a terminal",
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Exec(cmd.Context(), args)
		},
	}
	// idev exec -- ls -l の -l を idev のフラグと解釈させない。
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
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("Delete instance %s?", app.InstanceName())) {
				return errAborted
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

// confirm は破壊操作の確認を求める。
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	_, _ = fmt.Fprintf(out, "%s [y/N]: ", prompt)

	// EOF でも、読み取れた内容があれば回答として扱う（末尾改行なしの入力）。
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

// Execute はルートコマンドを実行する。
func Execute(ctx context.Context, version string, args []string) error {
	root := NewRootCommand(version)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// Report はエラーを出力し、プロセスの終了コードを返す。
//
// コンテナ内コマンドが異常終了しただけの場合は、その終了コードを返し、
// devkit自身のエラーとしては表示しない（出力は既に中継済みであるため）。
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

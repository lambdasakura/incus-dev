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
		Short: "Incusでプロジェクト単位の開発環境を構築・管理する",
		Long: "idev は .incus-dev/dev.yml に宣言された内容に従って\n" +
			"Incus instanceを用意し、宣言された手順を実行する。",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "詳細を出力する")
	pf.StringVarP(&g.directory, "directory", "C", "", "探索を開始するディレクトリ（既定: カレントディレクトリ）")
	pf.StringVar(&g.incusRemote, "incus-remote", "local", "Incus remote")
	pf.StringVar(&g.incusProject, "incus-project", "",
		"Incus project（未指定なら dev.yml の incus.project、それも無ければ default）")

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

	cmdRunner := runner.NewWithLogger(newLogger(os.Stderr, g.verbose))

	client, err := incus.Connect(target)
	if err != nil {
		return nil, err
	}

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
		Short: "instanceを用意し、bootstrapとprovisionを実行する",
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
		"実行予定の操作を表示するだけで、Incusへ変更を加えない")
	c.Flags().BoolVar(&restart, "restart", false,
		"反映に再起動が必要な変更があれば、instanceを再起動する")

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
		Short: "instanceを作り直さずにprovisionのみ再実行する",
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
		"指定したステップのみ実行する（名前または番号。複数指定可）")
	c.Flags().StringVar(&from, "from", "",
		"指定したステップ以降を実行する（名前または番号）")
	c.Flags().BoolVar(&listOnly, "list", false,
		"provisionステップの一覧を表示する（Incusへは触れない）")

	c.MarkFlagsMutuallyExclusive("step", "from")
	c.MarkFlagsMutuallyExclusive("step", "list")
	c.MarkFlagsMutuallyExclusive("from", "list")

	return c
}

func newShellCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "shell [-- command...]",
		Short: "コンテナ内でshell（または指定コマンド）を実行する",
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
		Short: "コンテナ内でコマンドを実行する（端末を割り当てない）",
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
		Short: "対象instanceの状態を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			return app.Status(cmd.Context(), asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "JSONで出力する")
	return c
}

func newDestroyCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var (
		force   bool
		volumes bool
	)
	c := &cobra.Command{
		Use:   "destroy",
		Short: "instanceを削除する（ホスト側のソースは削除しない）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("instance %s を削除します。よろしいですか?", app.InstanceName())) {
				return errAborted
			}
			return app.Destroy(cmd.Context(), DestroyOptions{Volumes: volumes})
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "確認せずに実行する")
	c.Flags().BoolVar(&volumes, "volumes", false, "永続ボリュームも削除する")

	return c
}

func newRebuildCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "rebuild",
		Short: "instanceを破棄して作り直す",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("instance %s を破棄して作り直します。よろしいですか?", app.InstanceName())) {
				return errAborted
			}
			return app.Rebuild(cmd.Context())
		},
	}
	c.Flags().BoolVarP(&force, "force", "f", false, "確認せずに実行する")
	return c
}

func newSnapshotCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	c := &cobra.Command{
		Use:   "snapshot",
		Short: "instanceのスナップショットを操作する",
	}

	c.AddCommand(&cobra.Command{
		Use:   "create [name]",
		Short: "スナップショットを作成する（名前を省略すると日時）",
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
		Short: "スナップショットの一覧を表示する",
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
		Short: "スナップショットの状態へ戻す（instance内の現在の状態は失われる）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !force && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("instance %s を %s の状態へ戻します。現在の状態は失われます。よろしいですか?",
					app.InstanceName(), args[0])) {
				return errAborted
			}
			return app.RestoreSnapshot(cmd.Context(), args[0])
		},
	}
	restore.Flags().BoolVarP(&force, "force", "f", false, "確認せずに実行する")
	c.AddCommand(restore)

	var deleteForce bool
	del := &cobra.Command{
		Use:   "delete <name>",
		Short: "スナップショットを削除する",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(g)
			if err != nil {
				return err
			}
			if !deleteForce && !confirm(cmd.InOrStdin(), cmd.OutOrStdout(),
				fmt.Sprintf("スナップショット %s を削除します。よろしいですか?", args[0])) {
				return errAborted
			}
			return app.DeleteSnapshot(cmd.Context(), args[0])
		},
	}
	del.Flags().BoolVarP(&deleteForce, "force", "f", false, "確認せずに実行する")
	c.AddCommand(del)

	return c
}

func newValidateCommand(g *globalFlags, newApp appFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "dev.ymlを検証する（Incusへは一切変更を加えない）",
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

// Package cli はコマンド定義と、Incus操作・ステップ実行のオーケストレーションを担当する。
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
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

// DefaultShellCommand は idev shell が起動するシェル。
// 将来的に dev.yml の shell.command で上書き可能にする（仕様 3.13）。
const DefaultShellCommand = "/bin/sh"

// AppOptions は App の構成。
type AppOptions struct {
	Config *config.Config
	Client incus.Client
	Runner runner.Runner
	// In は shell へ渡す標準入力。
	In io.Reader
	// Out は結果の出力先（status など）。
	Out io.Writer
	// ErrOut はログとステップ出力の出力先。
	ErrOut  io.Writer
	Verbose bool
	// Interactive は標準入出力が端末に接続されているか。
	// idev shell で擬似端末を割り当てるかの判断に使う。
	Interactive bool

	Remote       string
	IncusProject string

	// CheckIDMap は workspace.idmap: auto の事前検査。nilの場合は既定の検査を使う。
	CheckIDMap func(uid, gid int) error
	// Host はworkspaceの対応付けに使うホスト側のID。
	// nilの場合は実行ユーザーのものを使う。
	Host *HostIDs
}

// HostIDs はホスト側のuid/gid。
type HostIDs struct {
	UID, GID int
}

// App はコマンドの実処理を保持する。
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

	remote       string
	incusProject string

	checkIDMap func(uid, gid int) error
	host       HostIDs
}

// NewApp は App を構成する。
func NewApp(opt AppOptions) *App {
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
		instance:     incus.InstanceName(opt.Config.Project.Name),
		remote:       opt.Remote,
		incusProject: opt.IncusProject,
		checkIDMap:   checkIDMap,
		host:         *host,
	}
}

// InstanceName は対象instance名を返す。
func (a *App) InstanceName() string { return a.instance }

func (a *App) env() provision.Env {
	ws := a.cfg.WorkspaceOrDefault()
	return provision.Env{
		ProjectName:     a.cfg.Project.Name,
		ProjectRoot:     a.cfg.Root,
		Instance:        a.instance,
		Workspace:       ws.Target,
		WorkspaceSource: a.cfg.WorkspaceSourcePath(),
		Remote:          a.remote,
		IncusProject:    a.incusProject,
	}
}

// Up はinstanceを用意し、bootstrapとprovisionを実行する（仕様 04-cli.md 4.1）。
func (a *App) Up(ctx context.Context) error {
	a.log.Info("Project: " + a.cfg.Project.Name)

	// instanceを作る前に、ホスト側の前提を確認する。
	plan, err := a.idmapPlan()
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
		if err := a.reapplyInstance(ctx, inst, plan); err != nil {
			return err
		}
	case errors.Is(err, incus.ErrInstanceNotFound):
		a.log.Info("Creating instance " + a.instance)
		if err := a.client.CreateInstance(ctx, instanceSpec(a.cfg, a.instance, plan)); err != nil {
			return err
		}
		// deviceは作成時に設定済みなので、再適用は不要。
		created = true
	default:
		return err
	}

	ws := a.cfg.WorkspaceOrDefault()
	a.log.Info(fmt.Sprintf("Mounting workspace %s -> %s", a.cfg.WorkspaceSourcePath(), ws.Target))
	if !created {
		if err := a.client.ApplyDevices(ctx, a.instance, desiredDevices(a.cfg, plan)); err != nil {
			return err
		}
	}

	if err := a.ensureRunning(ctx); err != nil {
		return err
	}
	if err := a.runProvisioning(ctx); err != nil {
		return err
	}

	a.log.Info("Development environment is ready")
	return nil
}

// Provision はinstanceを作り直さず、bootstrapとprovisionのみ再実行する
// （仕様 04-cli.md 4.2）。
func (a *App) Provision(ctx context.Context) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if err := a.ensureRunning(ctx); err != nil {
		return err
	}
	return a.runProvisioning(ctx)
}

// Destroy はinstanceを削除する。ホスト側のソースには触れない。
func (a *App) Destroy(ctx context.Context) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	a.log.Info("Deleting instance " + a.instance)
	if err := a.client.DeleteInstance(ctx, a.instance); err != nil {
		return err
	}
	a.log.Info("Instance deleted. Source tree on the host is untouched")
	return nil
}

// Rebuild はinstanceを破棄して作り直す。
func (a *App) Rebuild(ctx context.Context) error {
	_, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		if err := a.Destroy(ctx); err != nil {
			return err
		}
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}
	return a.Up(ctx)
}

// Shell はコンテナ内でinteractive shell（または指定コマンド）を実行する。
func (a *App) Shell(ctx context.Context, argv []string) error {
	if _, err := a.managedInstance(ctx); err != nil {
		return err
	}
	if err := a.ensureRunning(ctx); err != nil {
		return err
	}
	if len(argv) == 0 {
		argv = []string{DefaultShellCommand}
	}

	opt := incus.ExecOptions{
		Cwd: a.cfg.WorkspaceOrDefault().Target,
		// 端末に接続されていない場合に擬似端末を割り当てると、
		// 出力へCRが混入しパイプやリダイレクトが壊れる。
		TTY: a.interactive,
	}
	if !opt.TTY {
		opt.Stdin = a.in
		opt.Stdout = a.out
		opt.Stderr = a.errOut
	}

	code, err := a.client.Exec(ctx, a.instance, argv, opt)
	if err != nil {
		// コマンドが異常終了しただけの場合は、その終了コードを伝播させる。
		// devkit自身のエラーとしては扱わない。
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

// statusReport は status の出力内容。
type statusReport struct {
	Project   string            `json:"project"`
	Instance  string            `json:"instance"`
	Status    string            `json:"status"`
	Image     string            `json:"image"`
	Workspace string            `json:"workspace"`
	Source    string            `json:"workspace_source"`
	Exists    bool              `json:"exists"`
	Managed   bool              `json:"managed"`
	Profiles  []string          `json:"profiles,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
	Steps     int               `json:"provision_steps"`
}

// Status は対象instanceの状態を表示する。
func (a *App) Status(ctx context.Context, asJSON bool) error {
	ws := a.cfg.WorkspaceOrDefault()
	report := statusReport{
		Project:   a.cfg.Project.Name,
		Instance:  a.instance,
		Status:    "NOT CREATED",
		Image:     a.cfg.Instance.Image,
		Workspace: ws.Target,
		Source:    a.cfg.WorkspaceSourcePath(),
		Steps:     len(a.cfg.Provision),
	}

	inst, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		report.Exists = true
		report.Status = inst.Status
		report.Managed = isManagedBy(inst.Config, a.cfg.Project.Name)
		report.Profiles = inst.Profiles
		report.Config = limitsOf(inst.Config)
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
			[2]string{"Managed", yesNo(r.Managed)},
		)
		for _, k := range sortedKeys(r.Config) {
			rows = append(rows, [2]string{k, r.Config[k]})
		}
	}
	rows = append(rows, [2]string{"Provision", fmt.Sprintf("%d step(s)", r.Steps)})

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

// Validate は設定が妥当であることを表示する。読み込み時点で検証済み。
func (a *App) Validate() error {
	_, err := fmt.Fprintf(a.out, "configuration is valid\nProject:    %s\nInstance:   %s\nProvision:  %d step(s)\n",
		a.cfg.Project.Name, a.instance, len(a.cfg.Provision))
	return err
}

// reapplyInstance は既存instanceへ宣言内容を再適用する。
func (a *App) reapplyInstance(ctx context.Context, inst *incus.Instance, plan idmapPlan) error {
	desired := desiredConfig(a.cfg, plan)
	// 適用前の状態を控える。適用後は差分が分からなくなるため。
	before := maps.Clone(inst.Config)

	// idmap方式を切り替えた場合、devkitが以前設定したキーを残さない。
	stale := staleIDMapKeys(inst.Config, plan)
	if len(stale) > 0 {
		if err := a.client.UnsetConfig(ctx, a.instance, stale); err != nil {
			return err
		}
	}
	if err := a.client.ApplyConfig(ctx, a.instance, desired); err != nil {
		return err
	}

	a.warnRestartRequired(inst.IsRunning(), before, desired, stale)
	return nil
}

// restartRequiredKeys は変更に再起動を要するconfigキー。
var restartRequiredKeys = []string{idmapConfigKey, "security.nesting", "security.privileged"}

// warnRestartRequired は稼働中instanceで即座に反映されない変更を警告する
// （仕様 05-incus.md 5.4.5）。
//
// devkitが実際に変更したキーのみを対象とする。宣言から消えたキーは
// unset しない方針（仕様 5.4.4）なので、それを変更として扱うと
// 何もしていないのに警告が出続けてしまう。
func (a *App) warnRestartRequired(running bool, before, desired map[string]string, unset []string) {
	if !running {
		return
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
	if len(changed) == 0 {
		return
	}
	slices.Sort(changed)
	changed = slices.Compact(changed)

	a.log.Warn(fmt.Sprintf(
		"%s changed but the instance is running; restart it to apply (idev rebuild --force, or incus restart %s)",
		strings.Join(changed, ", "), a.instance))
}

// idmapPlan は適用するidmap方針を解決する。
func (a *App) idmapPlan() (idmapPlan, error) {
	return resolveIDMap(a.cfg, a.host.UID, a.host.GID, a.checkIDMap)
}

// checkProfiles は指定Profileの存在を確認する。devkitはProfileを作成しない。
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

// managedInstance は対象instanceを取得し、devkit管理下であることを確認する。
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

// ensureRunning はinstanceを起動し、コマンドを実行できるまで待つ。
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
		// アドレスが現れない構成もありうるため、ここでは止めない。
		// ネットワークを使うステップが失敗したときの手がかりとして残す。
		a.log.Warn(err.Error() + "; steps that need network access may fail")
		return nil
	}
	return err
}

// runProvisioning はbootstrapとprovisionを順に実行する（仕様 06-provisioning.md 6.1）。
func (a *App) runProvisioning(ctx context.Context) error {
	env := a.env()
	if err := a.exec.Bootstrap(ctx, a.cfg, env); err != nil {
		return err
	}
	return a.exec.Provision(ctx, a.cfg, env)
}

// ExitCodeError は終了コードをそのまま伝播させたい場合に使う。
type ExitCodeError struct{ Code int }

func (e *ExitCodeError) Error() string { return fmt.Sprintf("exited with code %d", e.Code) }

// limitsOf は表示対象のconfigキーを抽出する。
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

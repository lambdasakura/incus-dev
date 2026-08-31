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
	"strconv"
	"strings"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/incus"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/provision"
	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/runner"
)

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
	// Term はホスト端末の種類（TERM）。擬似端末を割り当てる際にコンテナへ渡す。
	Term string

	Remote       string
	IncusProject string

	// CheckIDMap は workspace.idmap: auto の事前検査。nilの場合は既定の検査を使う。
	CheckIDMap func(uid, gid int) error
	// Host はworkspaceの対応付けに使うホスト側のID。
	// nilの場合は実行ユーザーのものを使う。
	Host *HostIDs
	// Branch は project.scope: branch のときに現在のブランチ名を返す。
	Branch branchFunc
	// LookupEnv は秘密情報をホストの環境変数から取り込む際に使う。
	// nilの場合は os.LookupEnv。
	LookupEnv func(string) (string, bool)
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
	term        string

	remote       string
	incusProject string

	checkIDMap func(uid, gid int) error
	host       HostIDs
	lookupEnv  func(string) (string, bool)
}

// NewApp は App を構成する。
//
// instance名の決定に失敗する場合（project.scope: branch でGitが使えないなど）は
// NewAppFor を使う。
func NewApp(opt AppOptions) *App {
	app, err := NewAppFor(opt)
	if err != nil {
		// 既定のscopeでは失敗しない。テストや既定構成のための簡便な入口。
		panic(err)
	}
	return app
}

// NewAppFor は App を構成する。instance名の決定に失敗した場合はエラーを返す。
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
		remote:       opt.Remote,
		incusProject: opt.IncusProject,
		checkIDMap:   checkIDMap,
		host:         *host,
		lookupEnv:    lookupEnv,
	}, nil
}

// InstanceName は対象instance名を返す。
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
		Remote:          a.remote,
		IncusProject:    a.incusProject,
		Secrets:         secrets,
	}, nil
}

// UpOptions は idev up の挙動。
type UpOptions struct {
	// Restart が真の場合、反映に再起動が必要な変更があれば再起動する。
	Restart bool
}

// Up はinstanceを用意し、bootstrapとprovisionを実行する（仕様 04-cli.md 4.1）。
func (a *App) Up(ctx context.Context, opt UpOptions) error {
	a.log.Info("Project: " + a.cfg.Project.Name)

	// instanceを作る前に、ホスト側の前提をすべて確認する。
	// 途中で失敗して中途半端なinstanceが残ることを避ける。
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
		// deviceは作成時に設定済みなので、再適用は不要。
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

// Provision はinstanceを作り直さず、bootstrapとprovisionのみ再実行する
// （仕様 04-cli.md 4.2）。
//
// sel が指定されていれば、provisionの一部だけを実行する。
func (a *App) Provision(ctx context.Context, sel provision.Selection) error {
	// 解決できない指定や、揃っていない前提は、instanceに触れる前に弾く。
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

// ListSteps はprovisionステップの一覧を表示する。
// --step / --from で指定できる名前を確認するために使う。
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

// stepKind はステップの種別を返す。
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

// DestroyOptions は idev destroy の挙動。
type DestroyOptions struct {
	// Volumes が真の場合、永続ボリュームも削除する。
	Volumes bool
}

// Destroy はinstanceを削除する。ホスト側のソースには触れない。
//
// 永続ボリュームは既定で残す。instanceを作り直しても残すためのものであり、
// 削除するかは利用者が明示的に決める（仕様 04-cli.md 4.5）。
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

// ensureVolumes は宣言された永続ボリュームを用意する。
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

// deleteVolumes は宣言された永続ボリュームを削除する。
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

// Rebuild はinstanceを破棄して作り直す。
func (a *App) Rebuild(ctx context.Context) error {
	_, err := a.client.Instance(ctx, a.instance)
	switch {
	case err == nil:
		// rebuildでは永続ボリュームを残す。作り直しても残すためのものなので。
		if err := a.Destroy(ctx, DestroyOptions{}); err != nil {
			return err
		}
	case !errors.Is(err, incus.ErrInstanceNotFound):
		return err
	}
	return a.Up(ctx, UpOptions{})
}

// Shell はコンテナ内でinteractive shell（または指定コマンド）を実行する。
func (a *App) Shell(ctx context.Context, argv []string) error {
	return a.execInContainer(ctx, argv, a.interactive)
}

// Exec はコンテナ内でコマンドを実行する。端末は割り当てない。
//
// スクリプトやCIから使うことを想定し、shellと違って対話にならない。
func (a *App) Exec(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("exec requires a command; use 'idev shell' for an interactive shell")
	}
	return a.execInContainer(ctx, argv, false)
}

// execInContainer はコンテナ内でコマンドを実行する。
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
		// 端末に接続されていない場合に擬似端末を割り当てると、
		// 出力へCRが混入しパイプやリダイレクトが壊れる。
		TTY:    tty,
		Term:   a.term,
		Stdin:  a.in,
		Stdout: a.out,
		Stderr: a.errOut,
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

// asUser は指定ユーザーで実行するためのargvと、Incusへ渡すユーザー指定を返す。
//
// Incusのexecはuidしか受け付けないため、ユーザー名の場合は
// コンテナ内で su を使って切り替える（run ステップと同じ扱い）。
func asUser(argv []string, sh config.Shell) (out []string, user string) {
	if sh.User == "" {
		return argv, ""
	}
	if _, err := strconv.Atoi(sh.User); err == nil {
		return argv, sh.User
	}

	return []string{"su", "-s", sh.Command, sh.User, "-c", strings.Join(argv, " ")}, ""
}

// statusReport は status の出力内容。
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
	Remote       string            `json:"incus_remote"`
	IncusProject string            `json:"incus_project"`
}

// Status は対象instanceの状態を表示する。
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
		Remote:       a.remote,
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
		[2]string{"Incus", incusTarget(r.Remote, r.IncusProject)},
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

// Validate は設定が妥当であることを表示する。読み込み時点で検証済み。
func (a *App) Validate() error {
	_, err := fmt.Fprintf(a.out, "configuration is valid\nProject:    %s\nInstance:   %s\nProvision:  %d step(s)\n",
		a.cfg.Project.Name, a.instance, len(a.cfg.Provision))
	return err
}

// reapplyInstance は既存instanceへ宣言内容を再適用する。
func (a *App) reapplyInstance(ctx context.Context, inst *incus.Instance, plan idmapPlan, opt UpOptions) error {
	desired := desiredConfig(a.cfg, plan)
	// 適用前の状態を控える。適用後は差分が分からなくなるため。
	before := maps.Clone(inst.Config)

	// 宣言から消えた、devkit適用済みのキーとdeviceを取り消す。
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

// settleRestart は再起動が必要な変更に対処する（仕様 05-incus.md 5.4.5）。
//
// 既定では警告に留める。利用者の作業中プロセスを予期せず止めないため、
// 再起動は明示的に指示された場合のみ行う。
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

// restartRequiredKeys は変更に再起動を要するconfigキー。
var restartRequiredKeys = []string{idmapConfigKey, "security.nesting", "security.privileged"}

// restartRequiredChanges は反映に再起動が必要な変更を返す。
//
// devkitが実際に変更・取り消したキーのみを対象とする。
// 触れていないキーを含めると、何もしていないのに警告が出続けてしまう。
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
//
// bootstrapは一部実行のときも省略しない。provisionerを動かすための
// 準備であり、軽量かつ冪等であることを前提としているため。
func (a *App) runProvisioning(ctx context.Context, env provision.Env, sel provision.Selection) error {
	if err := a.exec.Bootstrap(ctx, a.cfg, env); err != nil {
		return err
	}
	return a.exec.Provision(ctx, a.cfg, env, sel)
}

// ExitCodeError は終了コードをそのまま伝播させたい場合に使う。
type ExitCodeError struct{ Code int }

func (e *ExitCodeError) Error() string { return fmt.Sprintf("exited with code %d", e.Code) }

// deviceSummary は device を「名前(型)」の一覧にする。
func deviceSummary(devices map[string]incus.Device) []string {
	out := make([]string, 0, len(devices))
	for _, name := range slices.Sorted(maps.Keys(devices)) {
		out = append(out, fmt.Sprintf("%s(%s)", name, devices[name].Type()))
	}
	return out
}

// incusTarget は操作対象のremoteとprojectを示す。
func incusTarget(remote, project string) string {
	if remote == "" && project == "" {
		return ""
	}
	return fmt.Sprintf("%s / %s", orDefault(remote, "local"), orDefault(project, "default"))
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

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

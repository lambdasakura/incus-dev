package cli

import (
	"fmt"

	"gitlab.light-of-moe.com/sakura/incus-devkit/internal/config"
)

// idmapPlan は解決済みのuid/gid対応付け方針。
//
// 「どの方式を使うか」と「そもそもdevkitが管理するか」を1つの値にまとめ、
// 計画の算出（plan.go）と適用（app.go）が同じ判断を共有できるようにする。
type idmapPlan struct {
	// Mode は適用する方式。Managed が偽の場合は意味を持たない。
	Mode config.IDMapMode
	// Managed はdevkitが対応付けを管理するか。
	// 利用者が instance.config で raw.idmap を明示した場合や、
	// コンテナ以外のinstanceでは偽になる。
	Managed bool
	// UID / GID はホスト側の実行ユーザー。
	UID, GID int
	// Warning は利用者へ伝えるべき事項。空なら無し。
	Warning string
}

// shiftEnabled はdisk deviceに shift を設定すべきかを返す。
func (p idmapPlan) shiftEnabled() bool {
	return p.Managed && p.Mode == config.IDMapShift
}

// rawIDMap は raw.idmap へ設定すべき値を返す。設定しない場合は空。
func (p idmapPlan) rawIDMap() string {
	if !p.Managed || p.Mode != config.IDMapRaw {
		return ""
	}
	// uidとgidは異なりうるため個別に写像する。
	return fmt.Sprintf("uid %d 0\ngid %d 0", p.UID, p.GID)
}

// userManagesIDMap は利用者が自分で対応付けを指定しているかを返す。
func userManagesIDMap(cfg *config.Config) bool {
	_, explicit := cfg.Instance.Config[idmapConfigKey]
	return explicit
}

// resolveIDMap は適用するidmap方針を決める。
//
//   - 利用者が raw.idmap を明示している場合は介入しない
//   - コンテナ以外では対応付けの概念が異なるため介入しない
//   - auto : raw が使えれば raw、使えなければ shift へ退避（警告を返す）
//   - raw  : 使えない場合はエラー
//   - shift / none : そのまま
func resolveIDMap(cfg *config.Config, uid, gid int, check func(uid, gid int) error) (idmapPlan, error) {
	plan := idmapPlan{UID: uid, GID: gid}
	declared := cfg.WorkspaceOrDefault().IDMap

	if userManagesIDMap(cfg) {
		if cfg.Workspace != nil && cfg.Workspace.IDMap != "" {
			plan.Warning = fmt.Sprintf(
				"instance.config.%s is set, so workspace.idmap: %s is ignored", idmapConfigKey, declared)
		}
		return plan, nil
	}
	if cfg.Instance.TypeOrDefault() != "container" {
		// raw.idmap も disk の shift もコンテナ固有の仕組みである。
		return plan, nil
	}

	plan.Managed = true
	plan.Mode = declared

	switch declared {
	case config.IDMapRaw:
		if err := check(uid, gid); err != nil {
			return idmapPlan{}, err
		}
	case config.IDMapAuto:
		if err := check(uid, gid); err != nil {
			// ホストへ手を入れなくても動くよう、エラーとせずshiftへ退避する。
			plan.Mode = config.IDMapShift
			plan.Warning = fallbackWarning(uid, gid)
			//nolint:nilerr // 退避は意図した動作であり、警告として利用者へ伝える
			return plan, nil
		}
		plan.Mode = config.IDMapRaw
	}
	return plan, nil
}

// fallbackWarning は shift へ退避したことと、より良い設定方法を伝える。
func fallbackWarning(uid, gid int) string {
	return fmt.Sprintf(
		"workspace is mounted with shift (idmapped mount) because raw.idmap is not permitted on this host.\n"+
			"        Files created inside the container will be owned by root on the host.\n"+
			"        To have them owned by you, add 'root:%d:1' to %s and 'root:%d:1' to %s,\n"+
			"        then run 'idev up' again.",
		uid, subUIDPath, gid, subGIDPath)
}

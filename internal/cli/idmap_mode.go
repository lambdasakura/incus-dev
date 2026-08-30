package cli

import (
	"fmt"
	"os"

	"github.com/lambdasakura/incus-dev/internal/config"
)

// resolveIDMapMode は宣言されたidmap方式を、ホストの状況に応じて解決する。
//
//   - auto : raw が使えれば raw、使えなければ shift へ退避（警告を返す）
//   - raw  : 使えない場合はエラー
//   - shift / none : そのまま
//
// 戻り値の warn は、退避した場合に利用者へ示す警告文。
func resolveIDMapMode(declared config.IDMapMode, check func(uid, gid int) error) (config.IDMapMode, string, error) {
	switch declared {
	case config.IDMapRaw:
		if err := check(os.Getuid(), os.Getgid()); err != nil {
			return "", "", err
		}
		return config.IDMapRaw, "", nil

	case config.IDMapAuto:
		if err := check(os.Getuid(), os.Getgid()); err != nil {
			return config.IDMapShift, fallbackWarning(os.Getuid(), os.Getgid()), nil
		}
		return config.IDMapRaw, "", nil

	default:
		return declared, "", nil
	}
}

// fallbackWarning は shift へ退避したことと、より良い設定方法を伝える。
func fallbackWarning(uid, gid int) string {
	return fmt.Sprintf(
		"workspace is mounted with shift (idmapped mount) because raw.idmap is not permitted on this host.\n"+
			"        Files created inside the container will be owned by root on the host.\n"+
			"        To have them owned by you, add 'root:%d:1' to %s, 'root:%d:1' to %s,\n"+
			"        restart incus and run 'idev up' again.",
		uid, subUIDPath, gid, subGIDPath)
}

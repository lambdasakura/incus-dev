package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// subuid/subgid の既定の位置。
const (
	subUIDPath = "/etc/subuid"
	subGIDPath = "/etc/subgid"
)

// subIDRange は /etc/subuid, /etc/subgid の1エントリ。
type subIDRange struct {
	Owner string
	Start int64
	Count int64
}

// parseSubIDs は subuid/subgid 形式（owner:start:count）を読み取る。
func parseSubIDs(r io.Reader) []subIDRange {
	var out []subIDRange

	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 3 {
			continue
		}
		start, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		count, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, subIDRange{Owner: parts[0], Start: start, Count: count})
	}
	return out
}

// allowsID は owner に id の使用が許可されているかを返す。
func allowsID(ranges []subIDRange, owner string, id int64) bool {
	for _, r := range ranges {
		if r.Owner != owner {
			continue
		}
		if id >= r.Start && id < r.Start+r.Count {
			return true
		}
	}
	return false
}

// checkSubIDAllowed は raw.idmap による uid/gid の対応付けが
// ホスト側で許可されているかを確認する。
//
// 非特権コンテナでホストのuidを対応付けるには、incus daemon を動かす
// root に対して当該IDの使用が許可されている必要がある。
// 設定ファイルを読めない場合は判定できないため通す。
func checkSubIDAllowed(uidPath, gidPath string, uid, gid int) error {
	uidRanges, err := readSubIDs(uidPath)
	if err != nil {
		//nolint:nilerr // 設定ファイルを読めない環境では判定できないため通す
		return nil
	}
	gidRanges, err := readSubIDs(gidPath)
	if err != nil {
		//nolint:nilerr // 同上
		return nil
	}

	missing := make([]string, 0, 2)
	if !allowsID(uidRanges, "root", int64(uid)) {
		missing = append(missing, fmt.Sprintf("%s: root:%d:1", subUIDPath, uid))
	}
	if !allowsID(gidRanges, "root", int64(gid)) {
		missing = append(missing, fmt.Sprintf("%s: root:%d:1", subGIDPath, gid))
	}
	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf(
		"workspace idmap (raw.idmap) is not permitted on this host.\n"+
			"Incus needs permission to map your uid/gid into the container.\n\n"+
			"Add the following entries (no incus restart needed):\n  %s\n\n"+
			"Alternatively set 'workspace: {idmap: none}' in dev.yml and handle\n"+
			"ownership yourself (host files will not be writable from the container)",
		strings.Join(missing, "\n  "))
}

func readSubIDs(path string) ([]subIDRange, error) {
	f, err := os.Open(path) //nolint:gosec // 読み取り先は固定の /etc/subuid, /etc/subgid
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return parseSubIDs(f), nil
}

// defaultIDMapCheck は既定のidmap事前検査。
func defaultIDMapCheck(uid, gid int) error {
	return checkSubIDAllowed(subUIDPath, subGIDPath, uid, gid)
}

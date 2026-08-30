//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const idmapYAML = `
schema: 1
project:
  name: {{PROJECT}}
instance:
  image: {{IMAGE}}
`

// workspace.idmap: auto（既定）の挙動を、ホストの設定に応じて検証する。
//
//   - 許可されている場合: コンテナからworkspaceへ書き込め、ホスト側の所有者が実行ユーザーになる
//   - 許可されていない場合: instanceを作る前に、対処方法を含むエラーで失敗する
func TestWorkspaceIDMap(t *testing.T) {
	f := newFixture(t, idmapYAML)

	if !idmapPermitted(t) {
		out := f.mustFail("up")

		for _, want := range []string{"/etc/subuid", "idmap: none"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, %q を含むこと", out, want)
			}
		}
		if got := incusOut(t, "list", f.instance, "--format", "csv", "-c", "n"); got != "" {
			t.Errorf("検査前にinstanceを作成している: %q", got)
		}
		t.Logf("このホストは raw.idmap を許可していないため、失敗経路のみ検証しました")
		return
	}

	f.mustRun("up")
	f.mustRun("shell", "--", "sh", "-c", "echo written > /workspace/from-container.txt")

	path := filepath.Join(f.root, "from-container.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("コンテナからホスト側へ書き込めていない: %v", err)
	}
	if uid := fileUID(info); uid != os.Getuid() {
		t.Errorf("uid = %d, want %d (ホストの実行ユーザーが所有すること)", uid, os.Getuid())
	}
}

// fileUID はファイルの所有uidを返す。
func fileUID(info os.FileInfo) int {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}

// idmapPermitted は root が現在のuid/gidを対応付けられるかを返す。
func idmapPermitted(t *testing.T) bool {
	t.Helper()

	return subIDAllows(t, "/etc/subuid", os.Getuid()) && subIDAllows(t, "/etc/subgid", os.Getgid())
}

func subIDAllows(t *testing.T, path string, id int) bool {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ":")
		if len(parts) != 3 || parts[0] != "root" {
			continue
		}
		start, err1 := strconv.Atoi(parts[1])
		count, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if id >= start && id < start+count {
			return true
		}
	}
	return false
}

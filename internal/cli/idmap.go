package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Where subuid and subgid live by default.
const (
	subUIDPath = "/etc/subuid"
	subGIDPath = "/etc/subgid"
)

// subIDRange is one entry from /etc/subuid or /etc/subgid.
type subIDRange struct {
	Owner string
	Start int64
	Count int64
}

// parseSubIDs reads the subuid/subgid format, owner:start:count.
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

// allowsID reports whether owner is permitted to use id.
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

// checkSubIDAllowed verifies that the host permits mapping a uid and gid
// through raw.idmap.
//
// Mapping a host uid into an unprivileged container requires that root — which
// runs the incus daemon — be permitted to use that ID. When the configuration
// files cannot be read there is nothing to judge by, so it passes.
func checkSubIDAllowed(uidPath, gidPath string, uid, gid int) error {
	uidRanges, err := readSubIDs(uidPath)
	if err != nil {
		//nolint:nilerr // nothing to judge by when the file cannot be read, so pass
		return nil
	}
	gidRanges, err := readSubIDs(gidPath)
	if err != nil {
		//nolint:nilerr // as above
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
	f, err := os.Open(path) //nolint:gosec // the paths are the fixed /etc/subuid and /etc/subgid
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return parseSubIDs(f), nil
}

// defaultIDMapCheck is the default up-front idmap check.
func defaultIDMapCheck(uid, gid int) error {
	return checkSubIDAllowed(subUIDPath, subGIDPath, uid, gid)
}

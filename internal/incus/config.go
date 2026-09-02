package incus

import (
	"slices"
	"strings"
)

// IDMapKey is the instance config key carrying the uid/gid mapping.
const IDMapKey = "raw.idmap"

// RestartRequiredKeys are the config keys Incus reads only when the instance
// boots, so a change to one of them does not take effect until it restarts.
//
// limits.* is not among them: in a container both increases and decreases take
// effect while it runs. That is a statement about Incus, which is why it lives
// here and is checked against a real daemon rather than asserted in a comment.
var RestartRequiredKeys = []string{IDMapKey, "security.nesting", "security.privileged"}

// SameIDMapping reports whether two raw.idmap values ask the kernel for the
// same thing.
//
// idev used to write "both <id> 0" and now writes "uid <id> 0" and
// "gid <id> 0" on separate lines. Incus reads them as one mapping, so
// demanding a restart to respell it would cost every upgraded instance
// whatever was running inside it. That is a belief about Incus, and beliefs
// about Incus are what the contract exists to hold: see
// internal/incus/contract.
func SameIDMapping(want, have string) bool {
	return want == have || normalizeIDMap(want) == normalizeIDMap(have)
}

// normalizeIDMap rewrites a raw.idmap into one comparable form: sorted lines,
// with "both" expanded into its uid and gid halves.
func normalizeIDMap(value string) string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			// Not a shape idev writes; compare it as it is.
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
			continue
		}
		if fields[0] == "both" {
			lines = append(lines,
				"uid "+fields[1]+" "+fields[2],
				"gid "+fields[1]+" "+fields[2])
			continue
		}
		lines = append(lines, strings.Join(fields, " "))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

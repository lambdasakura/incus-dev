package incus

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// InstanceNamePrefix is the prefix of the instance names idev creates.
	// It is independent of the command name idev, and means "an instance for
	// development".
	InstanceNamePrefix = "dev-"
	// maxInstanceNameLength is the longest name Incus accepts.
	maxInstanceNameLength = 63
	// maxSuffixLength is how much of the name a suffix may take, so that the
	// prefix and part of the project name always fit beside it.
	maxSuffixLength = 32
)

// InstanceName derives an Incus instance name from a project name,
// normalising it to what Incus accepts: letters, digits and hyphens, at most
// 63 characters.
func InstanceName(projectName string) string {
	return InstanceNameWithSuffix(projectName, "")
}

// InstanceNameWithSuffix derives an instance name with a suffix.
//
// It tells several checkouts on one machine apart (spec 05-incus.md 5.1), so
// the suffix is what must survive: a long project name is shortened to make
// room for it rather than the other way round.
//
// The suffix can be anything — project.scope: branch passes a git branch name
// straight through — so an oversized one is replaced by a hash of itself,
// which keeps two long branches apart while leaving room for the prefix and
// something of the project name.
func InstanceNameWithSuffix(projectName, suffix string) string {
	if suffix == "" {
		return instanceName(projectName)
	}

	tail := normalize(suffix)
	if tail == "" || len(tail) > maxSuffixLength {
		// A suffix with no letters or digits — a branch named in Japanese, say
		// — would otherwise be dropped, putting two checkouts on one instance.
		// One that is too long would be cut, which can make two of them equal.
		tail = shortHash(suffix)
	}

	head := normalize(projectName)
	if head == "" {
		head = shortHash(projectName)
	}
	head = truncate(head, maxInstanceNameLength-len(InstanceNamePrefix)-1-len(tail))

	return InstanceNamePrefix + strings.TrimRight(head, "-") + "-" + tail
}

// ShortHash returns a short hexadecimal string that distinguishes a name.
func ShortHash(s string) string {
	return shortHash(s)
}

func instanceName(projectName string) string {
	name := InstanceNamePrefix + normalize(projectName)
	if normalize(projectName) == "" {
		// Names with no letters or digits would all normalise to the same
		// thing, so distinguish them with a short hash of the original.
		name = InstanceNamePrefix + shortHash(projectName)
	}
	return strings.TrimRight(truncate(name, maxInstanceNameLength), "-")
}

// normalize reduces a name to what Incus accepts: lowercase letters, digits
// and hyphens, with no run of hyphens and none at either end.
func normalize(name string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && sb.Len() > 0 {
				sb.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(sb.String(), "-")
}

// truncate shortens a name to at most n bytes.
//
// n is always positive at the call sites: a suffix is capped at
// maxSuffixLength, which leaves room for the prefix and part of the name.
func truncate(name string, n int) string {
	if len(name) > n {
		return name[:n]
	}
	return name
}

// shortHash returns a short hexadecimal string that distinguishes a name.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

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
)

// InstanceName derives an Incus instance name from a project name,
// normalising it to what Incus accepts: letters, digits and hyphens, at most
// 63 characters.
func InstanceName(projectName string) string {
	return InstanceNameWithSuffix(projectName, "")
}

// InstanceNameWithSuffix derives an instance name with a suffix.
//
// It tells several checkouts on one machine apart (spec 05-incus.md 5.1).
func InstanceNameWithSuffix(projectName, suffix string) string {
	if suffix != "" {
		projectName += "-" + suffix
	}
	return instanceName(projectName)
}

// ShortHash returns a short hexadecimal string that distinguishes a name.
func ShortHash(s string) string {
	return shortHash(s)
}

func instanceName(projectName string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(projectName) {
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
	normalized := strings.Trim(sb.String(), "-")
	if normalized == "" {
		// Names with no letters or digits would all normalise to the same
		// thing, so distinguish them with a short hash of the original.
		normalized = shortHash(projectName)
	}

	name := InstanceNamePrefix + normalized
	if len(name) > maxInstanceNameLength {
		name = name[:maxInstanceNameLength]
	}
	return strings.TrimRight(name, "-")
}

// shortHash returns a short hexadecimal string that distinguishes a name.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

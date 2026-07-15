package envversions

import (
	"regexp"
	"strconv"
	"strings"
)

// semver is a lightweight semantic-version representation good enough for
// comparing Rancher release tags (vX.Y.Z[-pre]) and Kubernetes distro
// versions (vX.Y.Z[-pre][+build], e.g. "v1.30.5+k3s1").
type semver struct {
	major, minor, patch int
	pre                 string // "" for a GA/release version
	raw                 string
}

var reSemver = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.]+))?(?:\+.*)?$`)

// parseSemver parses a version string, ignoring any build-metadata suffix
// (the part after "+"). Returns ok=false when the string doesn't look like a
// semantic version at all.
func parseSemver(v string) (semver, bool) {
	m := reSemver.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return semver{}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	return semver{major: major, minor: minor, patch: patch, pre: m[4], raw: v}, true
}

// majorMinor returns "major.minor" (e.g. "2.14").
func (s semver) majorMinor() string {
	return strconv.Itoa(s.major) + "." + strconv.Itoa(s.minor)
}

// less reports whether s orders strictly before o, using standard semver
// precedence: numeric major.minor.patch first, then a release (empty
// prerelease) always orders after any prerelease of the same
// major.minor.patch, otherwise prerelease strings compare lexicographically.
func (s semver) less(o semver) bool {
	if s.major != o.major {
		return s.major < o.major
	}
	if s.minor != o.minor {
		return s.minor < o.minor
	}
	if s.patch != o.patch {
		return s.patch < o.patch
	}
	if s.pre == o.pre {
		return false
	}
	if s.pre == "" {
		return false // release > any prerelease of the same core version
	}
	if o.pre == "" {
		return true // prerelease < release of the same core version
	}
	return prereleaseLess(s.pre, o.pre)
}

// rePrereleaseParts splits a prerelease string into a non-numeric label and a
// trailing numeric component, e.g. "rc10" -> ("rc", 10), "alpha" -> ("alpha",
// -1), "rc.3" -> ("rc.", 3).
var rePrereleaseParts = regexp.MustCompile(`^(.*?)(\d+)$`)

// prereleaseLess compares two prerelease identifiers with numeric awareness:
// identical labels order by their trailing number (so "rc10" > "rc2"), and
// differing labels order lexically. A missing trailing number sorts before
// any numbered variant of the same label.
func prereleaseLess(a, b string) bool {
	aLabel, aNum := splitPrerelease(a)
	bLabel, bNum := splitPrerelease(b)
	if aLabel == bLabel {
		return aNum < bNum
	}
	return aLabel < bLabel
}

// splitPrerelease returns the label and trailing number of a prerelease
// identifier. When there is no trailing number, num is -1 so an unnumbered
// label (e.g. "rc") sorts before "rc0"/"rc1".
func splitPrerelease(pre string) (label string, num int) {
	m := rePrereleaseParts.FindStringSubmatch(pre)
	if m == nil {
		return pre, -1
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return pre, -1
	}
	return m[1], n
}

// rePrerelease matches the informal prerelease markers used by Rancher and
// Kubernetes-distro version strings that aren't necessarily captured by the
// strict semver "-pre" component (e.g. appear elsewhere in the string).
var rePrerelease = regexp.MustCompile(`(?i)(rc\d*|alpha|beta|dev)`)

// isPrerelease reports whether a raw version string looks like a
// pre-release/RC build.
func isPrerelease(raw string) bool {
	return rePrerelease.MatchString(raw)
}

// sortSemverAsc sorts raw version strings that parse as semver in ascending
// order in place, dropping any that fail to parse. Returns the parsed+sorted
// slice.
func sortSemverAsc(raw []string) []semver {
	var parsed []semver
	for _, r := range raw {
		if sv, ok := parseSemver(r); ok {
			parsed = append(parsed, sv)
		}
	}
	for i := 1; i < len(parsed); i++ {
		for j := i; j > 0 && parsed[j].less(parsed[j-1]); j-- {
			parsed[j], parsed[j-1] = parsed[j-1], parsed[j]
		}
	}
	return parsed
}

// parseMaxKubeVersionConstraint extracts the exclusive upper bound from a
// Helm chart "kubeVersion" constraint string such as "< 1.35.0-0" or
// ">= 1.28.0 < 1.35.0-0". Returns ok=false when no upper bound is present.
func parseMaxKubeVersionConstraint(constraint string) (string, bool) {
	constraint = strings.ReplaceAll(constraint, "&lt;", "<")
	constraint = strings.ReplaceAll(constraint, "&gt;", ">")
	idx := strings.LastIndex(constraint, "<")
	if idx == -1 {
		return "", false
	}
	rest := strings.TrimSpace(constraint[idx+1:])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

// underMaxKubeVersion reports whether v is strictly less than the exclusive
// upper bound max (both semver-parseable strings, e.g. "1.30.5+k3s1" and
// "1.35.0-0").
func underMaxKubeVersion(v, max string) bool {
	vsv, ok := parseSemver(v)
	if !ok {
		return true // can't parse -> don't filter it out
	}
	maxsv, ok := parseSemver(max)
	if !ok {
		return true
	}
	return vsv.less(maxsv)
}

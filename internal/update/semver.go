package update

import (
	"fmt"
	"strconv"
	"strings"
)

// SemVer is a parsed semantic version. Build metadata is carried for display
// but ignored in precedence, as required by SemVer 2.0.0.
type SemVer struct {
	Major int
	Minor int
	Patch int
	Pre   []string
	Build []string
}

// ParseSemVer parses a canonical SemVer 2.0.0 string. A leading "v" is not
// accepted so a release string cannot have two spellings.
func ParseSemVer(raw string) (SemVer, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return SemVer{}, fmt.Errorf("empty version")
	}
	if s != raw {
		return SemVer{}, fmt.Errorf("version %q has surrounding whitespace", raw)
	}
	if strings.HasPrefix(s, "v") {
		return SemVer{}, fmt.Errorf("version %q must not start with v", raw)
	}

	core := s
	var build []string
	if i := strings.IndexByte(core, '+'); i >= 0 {
		buildPart := core[i+1:]
		core = core[:i]
		if buildPart == "" {
			return SemVer{}, fmt.Errorf("version %q has empty build metadata", raw)
		}
		build = strings.Split(buildPart, ".")
		for _, id := range build {
			if !validIdentifier(id) {
				return SemVer{}, fmt.Errorf("version %q has invalid build identifier %q", raw, id)
			}
		}
	}

	var pre []string
	if i := strings.IndexByte(core, '-'); i >= 0 {
		prePart := core[i+1:]
		core = core[:i]
		if prePart == "" {
			return SemVer{}, fmt.Errorf("version %q has empty prerelease", raw)
		}
		pre = strings.Split(prePart, ".")
		for _, id := range pre {
			if !validIdentifier(id) {
				return SemVer{}, fmt.Errorf("version %q has invalid prerelease identifier %q", raw, id)
			}
			if isNumeric(id) && len(id) > 1 && id[0] == '0' {
				return SemVer{}, fmt.Errorf("version %q has a numeric prerelease with a leading zero", raw)
			}
		}
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return SemVer{}, fmt.Errorf("version %q must have major.minor.patch", raw)
	}
	nums := make([]int, 3)
	for i, part := range parts {
		if !isNumeric(part) {
			return SemVer{}, fmt.Errorf("version %q has a non-numeric core component", raw)
		}
		if len(part) > 1 && part[0] == '0' {
			return SemVer{}, fmt.Errorf("version %q has a leading zero", raw)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return SemVer{}, fmt.Errorf("version %q has an invalid core component", raw)
		}
		nums[i] = n
	}
	return SemVer{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre, Build: build}, nil
}

func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// canonicalRelease parses a release version. Releases may carry prerelease
// identifiers but not build metadata: build metadata is ignored for
// precedence, so it cannot identify distinct immutable releases.
func canonicalRelease(raw string) (SemVer, error) {
	version, err := ParseSemVer(raw)
	if err != nil {
		return SemVer{}, err
	}
	if len(version.Build) > 0 {
		return SemVer{}, fmt.Errorf("release %q must not contain build metadata", raw)
	}
	if version.String() != raw {
		return SemVer{}, fmt.Errorf("release %q is not canonical", raw)
	}
	return version, nil
}

// String returns the canonical form.
func (v SemVer) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if len(v.Build) > 0 {
		s += "+" + strings.Join(v.Build, ".")
	}
	return s
}

// Compare returns -1, 0, or 1 when v is less than, equal to, or greater than
// other. Build metadata does not affect precedence.
func (v SemVer) Compare(other SemVer) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	if len(v.Pre) == 0 && len(other.Pre) == 0 {
		return 0
	}
	if len(v.Pre) == 0 {
		return 1
	}
	if len(other.Pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(other.Pre); i++ {
		a, b := v.Pre[i], other.Pre[i]
		an, bn := isNumeric(a), isNumeric(b)
		switch {
		case an && bn:
			if c := compareNumericIdentifier(a, b); c != 0 {
				return c
			}
		case an:
			return -1
		case bn:
			return 1
		default:
			if a < b {
				return -1
			}
			if a > b {
				return 1
			}
		}
	}
	return compareInt(len(v.Pre), len(other.Pre))
}

// compareNumericIdentifier compares non-negative decimal identifiers without
// converting them to fixed-width integers. SemVer does not bound numeric
// prerelease identifiers, so very long values must still sort correctly.
func compareNumericIdentifier(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

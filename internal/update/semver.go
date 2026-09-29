package update

import (
	"fmt"
	"strconv"
	"strings"
)

// semver is a parsed semantic version (semver.org 2.0.0); build
// metadata is dropped because it never affects precedence.
type semver struct {
	major, minor, patch int
	pre                 []string
}

// parseSemver accepts "1.2.3", "v1.2.3", "1.2.3-rc.1" and "1.2.3+meta".
func parseSemver(s string) (semver, error) {
	v := strings.TrimPrefix(strings.TrimSpace(s), "v")
	v, _, _ = strings.Cut(v, "+")
	base, pre, hasPre := strings.Cut(v, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("update: invalid version %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return semver{}, fmt.Errorf("update: invalid version %q", s)
		}
		nums[i] = n
	}
	out := semver{major: nums[0], minor: nums[1], patch: nums[2]}
	if hasPre {
		if pre == "" {
			return semver{}, fmt.Errorf("update: invalid version %q", s)
		}
		out.pre = strings.Split(pre, ".")
		for _, id := range out.pre {
			if id == "" {
				return semver{}, fmt.Errorf("update: invalid version %q", s)
			}
		}
	}
	return out, nil
}

// Compare orders two versions by semver precedence: -1 when a < b, 0 when
// equal, 1 when a > b. A "v" prefix is ignored, and a pre-release sorts
// before its release (1.2.0-rc.1 < 1.2.0).
func Compare(a, b string) (int, error) {
	va, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

func (a semver) compare(b semver) int {
	for _, d := range [3]int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := comparePreID(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(a.pre) - len(b.pre))
}

// comparePreID orders one dot-separated pre-release identifier: numeric
// ones numerically and below alphanumeric ones, which compare as ASCII.
func comparePreID(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return sign(na - nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// Newer reports whether candidate should be offered over current. A
// pre-release is never offered to someone on a stable release, even a
// higher one: 1.3.0-rc.1 does not replace 1.2.0.
func Newer(candidate, current string) (bool, error) {
	vc, err := parseSemver(candidate)
	if err != nil {
		return false, err
	}
	vr, err := parseSemver(current)
	if err != nil {
		return false, err
	}
	if len(vc.pre) > 0 && len(vr.pre) == 0 {
		return false, nil
	}
	return vc.compare(vr) > 0, nil
}

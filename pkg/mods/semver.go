package mods

import (
	"strconv"
	"strings"
)

// version is a semantic version as the mod catalogues use them (no "v"):
// MAJOR.MINOR.PATCH, an optional -prerelease and an optional +build, which
// doesn't count when versions are compared. It follows the launcher's
// core::semver, so a server picks the versions a player's launcher would.
type version struct {
	core [3]uint64
	pre  []ident
}

type ident struct {
	num   uint64
	word  string
	isNum bool
}

func parseVersion(text string) (version, bool) {
	if v, build, ok := strings.Cut(text, "+"); ok {
		for _, id := range strings.Split(build, ".") {
			if id == "" || !identChars(id) {
				return version{}, false
			}
		}
		text = v
	}
	core, pre, hasPre := strings.Cut(text, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var v version
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return version{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if id == "" || !identChars(id) {
				return version{}, false
			}
			if allDigits(id) {
				n, ok := number(id)
				if !ok {
					return version{}, false
				}
				v.pre = append(v.pre, ident{num: n, isNum: true})
			} else {
				v.pre = append(v.pre, ident{word: id})
			}
		}
	}
	return v, true
}

func identChars(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func number(s string) (uint64, bool) {
	if !allDigits(s) || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func (v version) prerelease() bool { return len(v.pre) > 0 }

func cmpU(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (a ident) cmp(b ident) int {
	switch {
	case a.isNum && b.isNum:
		return cmpU(a.num, b.num)
	case a.isNum:
		return -1
	case b.isNum:
		return 1
	}
	return strings.Compare(a.word, b.word)
}

// cmp orders versions as semver.org does (a prerelease before its release).
func (a version) cmp(b version) int {
	for i := range a.core {
		if c := cmpU(a.core[i], b.core[i]); c != 0 {
			return c
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
		if c := a.pre[i].cmp(b.pre[i]); c != 0 {
			return c
		}
	}
	return cmpU(uint64(len(a.pre)), uint64(len(b.pre)))
}

// newer reports whether version a is newer than b (both must parse).
func newer(a, b string) bool {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	return oka && okb && va.cmp(vb) > 0
}

// ParsesAsVersion reports whether text is a version InRange can compare.
func ParsesAsVersion(text string) bool {
	_, ok := parseVersion(text)
	return ok
}

// InRange reports whether ver is in range rng: space-separated comparators
// (>=, >, <=, <, =, ^, ~, or a bare version), all of which must hold; "*",
// "all" or nothing is every version. A prerelease is in a range only when
// prereleases is set or a comparator names a prerelease of the same core.
func InRange(ver, rng string, prereleases bool) bool {
	v, ok := parseVersion(ver)
	if !ok {
		return false
	}
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" || rng == "all" {
		return true
	}
	namedPre := false
	for _, part := range strings.Fields(rng) {
		op := ""
		for _, o := range []string{">=", "<=", ">", "<", "=", "^", "~"} {
			if strings.HasPrefix(part, o) {
				op = o
				break
			}
		}
		t, ok := parseVersion(part[len(op):])
		if !ok {
			return false
		}
		if t.prerelease() && t.core == v.core {
			namedPre = true
		}
		c := v.cmp(t)
		var hold bool
		switch op {
		case "", "=":
			namedPre = true
			hold = c == 0
		case ">=":
			hold = c >= 0
		case ">":
			hold = c > 0
		case "<=":
			hold = c <= 0
		case "<":
			hold = c < 0
		default:
			// ^ and ~: from the version up to (not including) the next breaking one.
			var upper [3]uint64
			switch {
			case op == "~":
				upper = [3]uint64{t.core[0], t.core[1] + 1, 0}
			case t.core[0] > 0:
				upper = [3]uint64{t.core[0] + 1, 0, 0}
			case t.core[1] > 0:
				upper = [3]uint64{0, t.core[1] + 1, 0}
			default:
				upper = [3]uint64{0, 0, t.core[2] + 1}
			}
			hold = c >= 0 && version{core: v.core}.cmp(version{core: upper}) < 0
		}
		if !hold {
			return false
		}
	}
	return !v.prerelease() || namedPre || prereleases
}

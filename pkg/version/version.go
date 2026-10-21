// Package version maps a tool version to the images released with it.
package version

import "strings"

// ImageTag is the image tag published together with tool version v: the
// release's own version (2.0.0, 2.0.0-rc.1), or "edge" for development
// builds, which use the images built from the branch.
func ImageTag(v string) string {
	v = strings.TrimPrefix(v, "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return "edge"
	}
	return v
}

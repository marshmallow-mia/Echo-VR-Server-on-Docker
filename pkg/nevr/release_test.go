package nevr

import "testing"

func TestPreReleaseOrder(t *testing.T) {
	for _, c := range [][2]string{{"v2.0.0-rc.2", "v2.0.0-rc.10"}, {"v2.0.0-beta.2", "v2.0.0-rc.1"}, {"v2.0.0-rc.1", "v2.0.0"}, {"v1.0.0-rc", "v1.0.0-rc.1"}} {
		a, b := MustParseVersion(c[0]), MustParseVersion(c[1])
		if !a.Less(b) || b.Less(a) {
			t.Errorf("want %s < %s", c[0], c[1])
		}
	}
}

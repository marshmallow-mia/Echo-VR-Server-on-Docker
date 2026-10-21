package version

import "testing"

func TestImageTag(t *testing.T) {
	for in, want := range map[string]string{
		"2.0.0-rc.1": "2.0.0-rc.1", "v2.0.0": "2.0.0", "dev": "edge", "": "edge", "edge-3af6301": "edge",
	} {
		if got := ImageTag(in); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

package nevr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"schema_version":1,"channels":{"beta":"6.17.0","stable":"6.16.5","Bad":"1.0.0","junk":"x"},"releases":[]}`))
	}))
	defer srv.Close()
	chs, err := (&Client{ManifestURL: srv.URL}).Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs["stable"] != "6.16.5" || chs["beta"] != "6.17.0" {
		t.Errorf("channels %v", chs)
	}
	if got := strings.Join(ChannelNames(map[string]string{"beta": "", "alpha": "", "stable": ""}), ","); got != "stable,alpha,beta" {
		t.Errorf("order %s", got)
	}
}

func TestSelfUpdates(t *testing.T) {
	for tag, want := range map[string]bool{"v6.16.5": false, "6.16.5": false, "v6.17.0": true, "6.18.1": true, "current": false} {
		if SelfUpdates(tag) != want {
			t.Errorf("%s: want %v", tag, want)
		}
	}
	for tag, want := range map[string]bool{"v6.16.5": false, "v6.17.0": false, "6.17.9": false, "v6.18.0": true, "6.18.1": true, "current": false} {
		if UpdateConfigurable(tag) != want {
			t.Errorf("UpdateConfigurable(%s): want %v", tag, want)
		}
	}
	for in, want := range map[string]string{"": "stable", "pre": "stable", "stable": "stable", "beta": "beta"} {
		if ChannelName(in) != want {
			t.Errorf("ChannelName(%q) = %q", in, ChannelName(in))
		}
	}
}

func TestPickableIsNewestFirstWithoutBlockedOrFailed(t *testing.T) {
	rels := []Release{{Tag: "v6.16.5"}, {Tag: "v6.18.1"}, {Tag: "v5.4.1", Blocked: "no"}, {Tag: "v6.17.0"}}
	installed := []Installed{{Tag: "v6.17.0", Bad: "canary failed"}, {Tag: "v6.9.2"}, {Tag: "v6.16.5"}}
	got := strings.Join(Pickable(rels, installed), " ")
	if want := "v6.18.1 v6.16.5 v6.9.2"; got != want {
		t.Fatalf("Pickable = %q, want %q", got, want)
	}
	if got := Pickable(nil, nil); len(got) != 0 {
		t.Fatalf("Pickable of nothing = %v", got)
	}
}

func TestPickLabel(t *testing.T) {
	chs := map[string]string{"stable": "6.16.5", "beta": "6.17.0", "alpha": "6.17.0"}
	for tag, want := range map[string]string{
		"v6.17.0": "v6.17.0 (nEVR alpha, nEVR beta, current)",
		"v6.16.5": "v6.16.5 (nEVR stable)",
		"v6.9.2":  "v6.9.2",
	} {
		if got := PickLabel(tag, chs, "v6.17.0"); got != want {
			t.Errorf("PickLabel(%s) = %q, want %q", tag, got, want)
		}
	}
}

func TestSameRelease(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v6.16.5", "6.16.5", true},
		{"v6.16.5-tel2100b", "6.16.5", true},
		{"v6.17.0", "6.17.0-dev.125+1a874753", true},
		{"v6.18.1", "6.16.5", false},
		{"current", "current", true},
		{"v6.16.5", "", false},
	} {
		if got := SameRelease(c.a, c.b); got != c.want {
			t.Errorf("SameRelease(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

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

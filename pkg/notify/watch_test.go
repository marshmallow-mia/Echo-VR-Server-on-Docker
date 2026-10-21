package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

func TestWatcherTransitions(t *testing.T) {
	var mu sync.Mutex
	var titles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Embeds []struct{ Title string } `json:"embeds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		titles = append(titles, body.Embeds[0].Title)
		mu.Unlock()
	}))
	defer srv.Close()
	w := &Watcher{Notifier: &Notifier{URL: srv.URL}}
	ctx := context.Background()
	obs := func(state string, st status.Status) []Observation {
		return []Observation{{Name: "echo-1", State: state, Status: st, Running: true, Enabled: true, Port: 6792}}
	}
	w.Observe(ctx, obs("registered", status.Status{}))                                             // baseline, no alert
	w.Observe(ctx, obs("failed", status.Status{Reason: "bad_credentials", LastError: "HTTP 401"})) // alert
	w.Observe(ctx, obs("registered", status.Status{PublicIP: "1.2.3.4", Port: 6792}))              // recovered
	w.Observe(ctx, obs("backoff", status.Status{Restarts: 6, LastError: "registration timeout"}))  // crash loop
	w.Observe(ctx, obs("in_session", status.Status{Restarts: 7, SessionsTotal: 1, LastError: ""})) // normal
	mu.Lock()
	defer mu.Unlock()
	want := []string{"echo-1: the backend rejected the account", "echo-1 is back", "echo-1 keeps restarting"}
	if len(titles) != len(want) {
		t.Fatalf("alerts %q, want %q", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Errorf("alert %d = %q, want %q", i, titles[i], want[i])
		}
	}
}

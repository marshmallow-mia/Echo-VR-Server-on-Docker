package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

func releases(t *testing.T, tags ...string) *httptest.Server {
	return releasesWith(t, map[string]string{"stable": strings.TrimPrefix(tags[0], "v")}, tags...)
}

// releasesWith serves GitHub's releases of tags and nEVR's manifest with channels.
func releasesWith(t *testing.T, channels map[string]string, tags ...string) *httptest.Server {
	t.Helper()
	zips := map[string][]byte{}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/nevr-runtime/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "channels": channels})
	})
	mux.HandleFunc("/repos/EchoTools/nevr-runtime/releases", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, tag := range tags {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			f, _ := zw.Create("x/BugSplat64.dll")
			f.Write([]byte(tag))
			zw.Close()
			zips[tag] = buf.Bytes()
			sum := sha256.Sum256(buf.Bytes())
			out = append(out, map[string]any{"tag_name": tag, "prerelease": true, "published_at": "2026-10-21T00:00:00Z",
				"assets": []map[string]any{{"name": "nevr-runtime-" + tag + ".zip", "digest": "sha256:" + hex.EncodeToString(sum[:]), "browser_download_url": srv.URL + "/dl/" + tag}}})
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) { w.Write(zips[strings.TrimPrefix(r.URL.Path, "/dl/")]) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeFleet simulates servers; a pinned server reports the pinned version
// and either registers or fails.
type fakeFleet struct {
	mu      sync.Mutex
	st      []status.Status
	works   bool
	pins    []string
	current func() string
}

func (f *fakeFleet) Statuses(context.Context) ([]status.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]status.Status(nil), f.st...), nil
}

func (f *fakeFleet) Pin(_ context.Context, instance, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, instance+"="+tag)
	for i := range f.st {
		if f.st[i].Instance != instance {
			continue
		}
		if tag == "" {
			f.st[i].RuntimeVersion, f.st[i].State = strings.TrimPrefix(f.current(), "v"), status.StateRegistered
		} else if f.works {
			f.st[i].RuntimeVersion, f.st[i].State = strings.TrimPrefix(tag, "v"), status.StateRegistered
		} else {
			f.st[i].RuntimeVersion, f.st[i].State, f.st[i].LastError = strings.TrimPrefix(tag, "v"), status.StateFailed, "boom"
		}
	}
	return nil
}

func setup(t *testing.T, works bool) (*Updater, *fakeFleet) {
	t.Helper()
	srv := releases(t, "v5.4.3", "v5.4.2")
	c := &nevr.Client{BaseURL: srv.URL, ManifestURL: srv.URL + "/nevr-runtime/manifest.json"}
	store := nevr.Store{Dir: filepath.Join(t.TempDir(), "nevr")}
	rel, err := c.Find(context.Background(), "v5.4.2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	store.Use("v5.4.2")
	fleet := &fakeFleet{works: works, current: store.Current, st: []status.Status{
		{Instance: "echo-1", State: status.StateInSession, RuntimeVersion: "5.4.2"},
		{Instance: "echo-2", State: status.StateRegistered, RuntimeVersion: "5.4.2"},
	}}
	return &Updater{Store: store, Client: c, Fleet: fleet, CanaryTimeout: 2 * time.Second, Poll: 10 * time.Millisecond}, fleet
}

func TestRolloutSwitchesAfterCanary(t *testing.T) {
	u, fleet := setup(t, true)
	res, err := u.Run(context.Background(), Policy{Channel: "pre"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "switched" || res.Canary != "echo-2" || u.Store.Current() != "v5.4.3" || u.Store.Previous() != "v5.4.2" {
		t.Fatalf("%+v current=%s", res, u.Store.Current())
	}
	if strings.Join(fleet.pins, ",") != "echo-2=v5.4.3,echo-2=" {
		t.Errorf("pins %v (the server in a match must not be touched)", fleet.pins)
	}
	if res, _ := u.Run(context.Background(), Policy{Channel: "pre"}); res.Action != "up_to_date" {
		t.Errorf("second run: %+v", res)
	}
}

func TestRolloutRollsBackOnCanaryFailure(t *testing.T) {
	u, fleet := setup(t, false)
	res, err := u.Run(context.Background(), Policy{Channel: "pre"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "rolled_back" || u.Store.Current() != "v5.4.2" || !strings.Contains(res.Detail, "boom") {
		t.Fatalf("%+v current=%s", res, u.Store.Current())
	}
	if inst, _ := u.Store.Get("v5.4.3"); inst.Bad == "" {
		t.Error("failed version not marked bad")
	}
	if fleet.pins[len(fleet.pins)-1] != "echo-2=" {
		t.Errorf("canary not unpinned: %v", fleet.pins)
	}
	if _, _, available, _ := u.Check(context.Background(), Policy{Channel: "pre"}); available {
		t.Error("a version marked bad must not be offered again")
	}
}

func TestRolloutWaitsForIdleServer(t *testing.T) {
	u, fleet := setup(t, true)
	fleet.st[1].State = status.StateInSession
	u.CanaryTimeout = 100 * time.Millisecond
	res, err := u.Run(context.Background(), Policy{Channel: "pre"})
	if err != nil || res.Action != "installed_only" || u.Store.Current() != "v5.4.2" {
		t.Fatalf("%+v %v", res, err)
	}
}

// The target is the release nEVR's channel names, not GitHub's newest, and
// it may be older than current: a channel that moves back is followed.
// An agent stopped while the canary starts leaves the release unmarked and
// the canary back on current.
func TestCanaryStoppedIsNotAFailure(t *testing.T) {
	u, fleet := setup(t, false) // the canary never registers
	u.CanaryTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			fleet.mu.Lock()
			n := len(fleet.pins)
			fleet.mu.Unlock()
			if n > 0 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res, err := u.Run(ctx, Policy{Channel: "pre"})
	if err == nil || res.Action != "installed_only" || u.Store.Current() != "v5.4.2" {
		t.Fatalf("%+v err %v current=%s", res, err, u.Store.Current())
	}
	if inst, _ := u.Store.Get("v5.4.3"); inst.Bad != "" {
		t.Errorf("marked bad: %s", inst.Bad)
	}
	if strings.Join(fleet.pins, ",") != "echo-2=v5.4.3,echo-2=" {
		t.Errorf("pins %v: the canary must go back to current", fleet.pins)
	}
}

// followingFleet is a fakeFleet that knows which servers run current.
type followingFleet struct {
	*fakeFleet
	follows map[string]bool
}

func (f followingFleet) FollowsCurrent(context.Context) (map[string]bool, error) {
	return f.follows, nil
}

func TestCanarySkipsPinnedServers(t *testing.T) {
	u, fleet := setup(t, true)
	fleet.st = append([]status.Status{{Instance: "pinned", State: status.StateRegistered, RuntimeVersion: "5.4.1"}}, fleet.st...)
	u.Fleet = followingFleet{fleet, map[string]bool{"echo-1": true, "echo-2": true}}
	res, err := u.Run(context.Background(), Policy{Channel: "pre"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "switched" || res.Canary != "echo-2" {
		t.Fatalf("%+v", res)
	}
	if strings.Join(fleet.pins, ",") != "echo-2=v5.4.3,echo-2=" {
		t.Errorf("pins %v (the pinned server must keep its own release)", fleet.pins)
	}
}

func TestNoServerFollowsCurrent(t *testing.T) {
	u, fleet := setup(t, true)
	u.Fleet = followingFleet{fleet, map[string]bool{"echo-1": false, "echo-2": false}}
	res, err := u.Run(context.Background(), Policy{Channel: "pre"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "switched" || res.Canary != "" || u.Store.Current() != "v5.4.3" || len(fleet.pins) != 0 {
		t.Fatalf("%+v current=%s pins=%v", res, u.Store.Current(), fleet.pins)
	}
}

func TestTargetFollowsNevrChannel(t *testing.T) {
	srv := releasesWith(t, map[string]string{"stable": "6.16.5", "beta": "6.17.0"}, "v6.18.1", "v6.17.0", "v6.16.5")
	u := &Updater{Client: &nevr.Client{BaseURL: srv.URL, ManifestURL: srv.URL + "/nevr-runtime/manifest.json"},
		Store: nevr.Store{Dir: filepath.Join(t.TempDir(), "nevr")}}
	for ch, want := range map[string]string{"": "v6.16.5", "pre": "v6.16.5", "stable": "v6.16.5", "beta": "v6.17.0"} {
		rel, err := u.Target(context.Background(), Policy{Channel: ch})
		if err != nil || rel.Tag != want {
			t.Errorf("channel %q: %s %v, want %s", ch, rel.Tag, err, want)
		}
	}
	if _, err := u.Target(context.Background(), Policy{Channel: "alpha"}); err == nil || !strings.Contains(err.Error(), "stable, beta") {
		t.Errorf("unknown channel: %v", err)
	}
	if rel, err := u.Target(context.Background(), Policy{Channel: "pinned", Pinned: "v6.18.1"}); err != nil || rel.Tag != "v6.18.1" {
		t.Errorf("pinned: %s %v", rel.Tag, err)
	}
	// A host on GitHub's newest (6.18.1) is offered stable's 6.16.5.
	rel, _ := u.Client.Find(context.Background(), "v6.18.1")
	u.Store.Install(context.Background(), rel)
	u.Store.Use("v6.18.1")
	if cur, target, available, err := u.Check(context.Background(), Policy{Channel: "stable"}); err != nil || !available || cur != "v6.18.1" || target.Tag != "v6.16.5" {
		t.Errorf("downgrade: %s → %s available=%v %v", cur, target.Tag, available, err)
	}
}

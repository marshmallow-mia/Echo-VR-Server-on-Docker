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

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/status"
)

func releases(t *testing.T, tags ...string) *httptest.Server {
	t.Helper()
	zips := map[string][]byte{}
	var srv *httptest.Server
	mux := http.NewServeMux()
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
	c := &nevr.Client{BaseURL: srv.URL}
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

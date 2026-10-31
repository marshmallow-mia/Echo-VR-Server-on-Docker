package mods

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// fakeFleet keeps locks in memory; a restart "starts" the server with its lock,
// and fail names a mod id whose new version fails to load.
type fakeFleet struct {
	mu       sync.Mutex
	locks    map[string]Lock
	sts      map[string]status.Status
	fail     string
	restarts []string
}

func (f *fakeFleet) Statuses(context.Context) ([]status.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []status.Status
	for _, s := range f.sts {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeFleet) ReadLock(s string) (Lock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.locks[s], nil
}

func (f *fakeFleet) WriteLock(s string, l Lock) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locks[s] = l
	return nil
}

func (f *fakeFleet) Restart(_ context.Context, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, s)
	st := status.Status{Instance: s, State: status.StateRegistered, RuntimeVersion: "6.9.0"}
	for _, m := range f.locks[s].Mods {
		sm := status.Mod{ID: m.ID, Version: m.Version, Files: m.Plugins, Loaded: true}
		if m.ID == f.fail && m.Version != "0.4.1" {
			sm.Loaded, sm.Error = false, "init failed code=3"
			st.State = status.StateFailed
		}
		st.Mods = append(st.Mods, sm)
	}
	f.sts[s] = st
	return nil
}

// withNewPatches publishes server-patches 0.4.2 beside 0.4.1.
func withNewPatches(t *testing.T, f *feed) {
	t.Helper()
	var doc map[string]any
	json.Unmarshal(f.files["launcher/mods.json"], &doc)
	b := []byte("MZ patches 0.4.2")
	f.files["launcher/mods/NvrServerPatches-0.4.2.dll"] = b
	doc["mods"] = append(doc["mods"].([]any), map[string]any{"id": "server-patches", "name": "Server Patches", "version": "0.4.2",
		"file": "NvrServerPatches.dll", "url": "launcher/mods/NvrServerPatches-0.4.2.dll", "sha256": sha256Hex(b), "target": "server"})
	f.files["launcher/mods.json"], _ = json.Marshal(doc)
	f.sign("", "launcher/mods.json", "launcher/catalogue.json", "launcher/rollback.json")
}

func rolloutRig(t *testing.T, fail string) (*feed, Host, *fakeFleet, []ServerMods) {
	f, src, st := fetchFrom(t)
	fl := &fakeFleet{locks: map[string]Lock{}, sts: map[string]status.Status{}, fail: fail}
	h := Host{Source: src, Store: st, Fleet: fl, Poll: time.Millisecond, CanaryTimeout: 2 * time.Second}
	servers := []ServerMods{
		{Name: "a", Specs: []Spec{{ID: "server-patches"}}, Runtime: "v6.9.0"},
		{Name: "b", Specs: []Spec{{ID: "server-patches"}, {ID: "rules"}}, Runtime: "v6.9.0"},
	}
	// Both run 0.4.1 to begin with: an edit writes it, then they start.
	plans, _, err := h.Plan(context.Background(), servers, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Download(context.Background(), plans); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Write(plans); err != nil {
		t.Fatal(err)
	}
	for _, s := range servers {
		fl.Restart(context.Background(), s.Name)
	}
	fl.restarts = nil
	return f, h, fl, servers
}

func TestRolloutGoesThroughACanary(t *testing.T) {
	f, h, fl, servers := rolloutRig(t, "")
	withNewPatches(t, f)
	// An edit keeps what runs: 0.4.2 waits for the update.
	plans, _, _ := h.Plan(context.Background(), servers, true)
	for _, p := range plans {
		if p.Changed() {
			t.Fatalf("an edit moved %s: %+v", p.Server, p.New.Versions())
		}
	}
	res, err := h.Rollout(context.Background(), servers)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "switched" || len(res.Fresh) != 1 || res.Fresh[0] != "server-patches 0.4.2" || res.Canary == "" || len(fl.restarts) != 1 {
		t.Fatalf("%+v restarts %v", res, fl.restarts)
	}
	for _, s := range servers {
		if v := fl.locks[s.Name].Versions()["server-patches"]; v != "0.4.2" {
			t.Errorf("%s runs %s", s.Name, v)
		}
	}
	// Nothing new: up to date.
	if res, _ := h.Rollout(context.Background(), servers); res.Action != "up_to_date" {
		t.Errorf("second rollout: %+v", res)
	}
}

func TestRolloutMarksAFailedVersionBad(t *testing.T) {
	f, h, fl, servers := rolloutRig(t, "server-patches")
	withNewPatches(t, f)
	res, err := h.Rollout(context.Background(), servers)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "rolled_back" || !strings.Contains(res.Detail, "init failed") || len(fl.restarts) != 2 {
		t.Fatalf("%+v restarts %v", res, fl.restarts)
	}
	for _, s := range servers {
		if v := fl.locks[s.Name].Versions()["server-patches"]; v != "0.4.1" {
			t.Errorf("%s runs %s after the rollback", s.Name, v)
		}
	}
	if in, _ := h.Store.Get("server-patches", "0.4.2"); in.Bad == "" {
		t.Error("0.4.2 isn't marked bad")
	}
	// The next check stays on 0.4.1.
	if res, _ := h.Rollout(context.Background(), servers); res.Action != "up_to_date" {
		t.Errorf("after the rollback: %+v", res)
	}
}

func TestRolloutWithoutRunningServersWrites(t *testing.T) {
	f, h, fl, servers := rolloutRig(t, "")
	fl.sts = map[string]status.Status{}
	withNewPatches(t, f)
	res, err := h.Rollout(context.Background(), servers)
	if err != nil || res.Action != "switched" || len(fl.restarts) != 0 || fl.locks["a"].Versions()["server-patches"] != "0.4.2" {
		t.Errorf("%+v %v restarts %v", res, err, fl.restarts)
	}
}

func TestAllLoadedOnFive(t *testing.T) {
	if !allLoaded(status.Status{RuntimeVersion: "v5.4.3", Mods: []status.Mod{{ID: "x"}}}) {
		t.Error("5.x prints no load results: registering is enough")
	}
	if allLoaded(status.Status{RuntimeVersion: "6.0.0-beta.1", Mods: []status.Mod{{ID: "x"}}}) {
		t.Error("a 6.0 prerelease says which loaded")
	}
}

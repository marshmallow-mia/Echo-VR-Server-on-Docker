package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

func setDeviceLink(t *testing.T, flag bool, env string) {
	t.Helper()
	old := DeviceLinkFlow
	DeviceLinkFlow = flag
	t.Setenv(DeviceLinkEnv, env)
	t.Cleanup(func() { DeviceLinkFlow = old })
}

func TestDeviceLinkEnabled(t *testing.T) {
	for _, c := range []struct {
		flag bool
		env  string
		want bool
	}{
		{false, "", false},
		{false, "0", false},
		{false, "maybe", false},
		{true, "", true},
		{false, "1", true},
		{false, "true", true},
	} {
		setDeviceLink(t, c.flag, c.env)
		if got := DeviceLinkEnabled(); got != c.want {
			t.Errorf("flag %v env %q: got %v, want %v", c.flag, c.env, got, c.want)
		}
	}
}

// Without a password the setup goes on: each server signs in by the code it
// prints on its console (nevr-runtime v6.9.0+). Nothing asks the backend.
func TestInitWithoutPasswordUsesTheConsoleCode(t *testing.T) {
	setDeviceLink(t, false, "")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := hostcfg.New()
	cfg.Defaults.HTTPURI = srv.URL // never the real backend
	cfg.DataDir = t.TempDir()
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	var an Answers
	an.Game.Source = "skip"
	var said []string
	a, err := Init(context.Background(), dir, an, func(_, d string) { said = append(said, d) })
	if err != nil || hits.Load() != 0 {
		t.Fatalf("got %v after %d request(s), want a setup and no request", err, hits.Load())
	}
	if !a.HasAccount() || !a.SignsInByCode() {
		t.Errorf("HasAccount %v SignsInByCode %v", a.HasAccount(), a.SignsInByCode())
	}
	if !strings.Contains(strings.Join(said, "\n"), "echovrce.com/device") {
		t.Errorf("setup did not say where to approve the code: %q", said)
	}
	if _, err := a.Login(context.Background()); !errors.Is(err, ErrSignInByCode) || hits.Load() != 0 {
		t.Errorf("Login without a password: %v after %d request(s)", err, hits.Load())
	}
}

// A password needs the Discord ID; without a password nothing is missing.
func TestSecretsMissing(t *testing.T) {
	for _, c := range []struct {
		sec  hostcfg.Secrets
		want int
	}{
		{hostcfg.Secrets{}, 0},
		{hostcfg.Secrets{config.EnvDiscordID: "1"}, 0},
		{hostcfg.Secrets{config.EnvPassword: "x"}, 1},
		{hostcfg.Secrets{config.EnvPassword: "x", config.EnvDiscordID: "1"}, 0},
	} {
		if got := c.sec.Missing(); len(got) != c.want {
			t.Errorf("%v: missing %v", c.sec, got)
		}
	}
}

// UseSignInCode drops the password (and the sign-ins it left in the servers),
// so the next start prints a code.
func TestUseSignInCode(t *testing.T) {
	setDeviceLink(t, false, "")
	dir := t.TempDir()
	cfg := hostcfg.New()
	cfg.DataDir = t.TempDir()
	cfg.Instances = []hostcfg.Instance{{Name: "evr-1", Port: 6792}}
	a := &App{Dir: dir, Cfg: cfg, Secrets: hostcfg.Secrets{config.EnvDiscordID: "1", config.EnvPassword: "x", config.EnvHTTPKey: "k"}}
	creds := rootCredentials(cfg.InstanceDir("evr-1"))
	os.MkdirAll(filepath.Dir(creds), 0o755)
	os.WriteFile(creds, []byte("{}"), 0o600)
	if err := a.dropPassword(); err != nil {
		t.Fatal(err)
	}
	if a.Secrets[config.EnvPassword] != "" || a.Secrets[config.EnvDiscordID] != "" || a.Secrets[config.EnvHTTPKey] != "k" {
		t.Errorf("secrets %v", a.Secrets)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "secrets.env")); strings.Contains(string(b), "NEVR_PASSWORD") {
		t.Errorf("password still saved: %s", b)
	}
	if _, err := os.Stat(creds); !os.IsNotExist(err) {
		t.Error("the password's sign-in kept in the root")
	}
	// Already on the code: a server's approved sign-in is kept.
	os.WriteFile(creds, []byte("{}"), 0o600)
	if err := a.dropPassword(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(creds); err != nil {
		t.Error("an approved code's sign-in was removed")
	}
	// The old host-wide device link goes, with the copies it left.
	legacy := filepath.Join(cfg.InstanceDir("evr-1"), "root", "_local", ".credentials.json")
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(legacy, []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, DeviceFile), []byte("{}"), 0o600)
	if err := a.dropPassword(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, DeviceFile), legacy, creds} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s kept after leaving the device link", p)
		}
	}
	if !a.SignsInByCode() {
		t.Error("not on the console code after account code")
	}
}

func TestLinkDeviceOff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := hostcfg.New()
	cfg.Defaults.HTTPURI = srv.URL
	a := &App{Dir: t.TempDir(), Cfg: cfg, Secrets: hostcfg.Secrets{}}

	setDeviceLink(t, false, "")
	_, err := a.UseDeviceLink(context.Background(), nil, func(string) {})
	if !errors.Is(err, ErrDeviceLinkOff) || hits.Load() != 0 {
		t.Fatalf("without the switch: got %v after %d request(s), want ErrDeviceLinkOff and none", err, hits.Load())
	}

	setDeviceLink(t, false, "1")
	if _, err := a.UseDeviceLink(context.Background(), nil, func(string) {}); errors.Is(err, ErrDeviceLinkOff) || hits.Load() == 0 {
		t.Fatalf("with %s=1 the link must start: got %v after %d request(s)", DeviceLinkEnv, err, hits.Load())
	}
}

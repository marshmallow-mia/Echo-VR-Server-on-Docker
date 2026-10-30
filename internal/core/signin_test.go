package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
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

func TestInitWithoutPasswordNeedsTheSwitch(t *testing.T) {
	setDeviceLink(t, false, "")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfg := hostcfg.New()
	cfg.Defaults.HTTPURI = srv.URL // never the real backend, even when the gate is broken
	cfg.DataDir = t.TempDir()
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(dir)
	var an Answers
	an.Game.Source = "skip"
	_, err := Init(context.Background(), dir, an, nil)
	if !errors.Is(err, ErrPasswordRequired) || hits.Load() != 0 {
		t.Fatalf("got %v after %d request(s), want ErrPasswordRequired and none", err, hits.Load())
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Fatalf("Init changed %s before refusing: %v", dir, after)
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

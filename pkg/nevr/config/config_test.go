package config

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestRenderGolden(t *testing.T) {
	c := Default()
	if err := c.SetMatchmaking([]string{"euw", "default"}, []string{"147afc9d-2819-4197-926d-5b3f92790edc"}); err != nil {
		t.Fatal(err)
	}
	c.Network.EmptyServerTTLSeconds = 30
	got, err := c.Render()
	if err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/default.golden.yaml"
	if *update {
		os.WriteFile(golden, got, 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("render mismatch (run with -update):\n%s", got)
	}
	back, err := Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if back.Services.SocketURI != c.Services.SocketURI || back.Network.TickRateHz != 120 || len(back.Guilds) != 1 {
		t.Errorf("round trip lost data: %+v", back)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Config){
		"no socket":       func(c *Config) { c.Services.SocketURI = "" },
		"http socket":     func(c *Config) { c.Services.SocketURI = "http://x/spr" },
		"no password":     func(c *Config) { c.Auth.Password = "" },
		"tick rate":       func(c *Config) { c.Network.TickRateHz = 5 },
		"ttl":             func(c *Config) { c.Network.EmptyServerTTLSeconds = 90000 },
		"router":          func(c *Config) { c.Network.SessionRouter = "fast" },
		"ipv6 external":   func(c *Config) { c.Network.ExternalIP = "::1" },
		"region w/ comma": func(c *Config) { c.Network.Regions = []string{"a,b"} },
	}
	for name, mutate := range cases {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("default config invalid: %v", err)
	}
}

func TestParseRejectsUnknownTopLevel(t *testing.T) {
	_, err := Parse([]byte("regions: [euw]\nservices: {socket_uri: ws://x}\n"))
	if err == nil || !strings.Contains(err.Error(), "regions") {
		t.Errorf("want unknown key error, got %v", err)
	}
	if _, err := Parse([]byte("x-notes: hi\nversion: 1\n")); err != nil {
		t.Errorf("x- keys must be allowed: %v", err)
	}
}

func TestParseListForms(t *testing.T) {
	c, err := Parse([]byte("network:\n  regions: \"uwu, euw\"\nguilds: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Network.Regions) != 2 || c.Network.Regions[1] != "euw" || len(c.Guilds) != 2 {
		t.Errorf("%+v %+v", c.Network.Regions, c.Guilds)
	}
}

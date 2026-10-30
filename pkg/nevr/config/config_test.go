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

func TestRootDefault(t *testing.T) {
	r := RootDefault()
	if err := r.SetMatchmaking([]string{"uwu", "default"}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, unwanted := range []string{"services:", "socket_uri", "serverdb_uri", "http_uri", "http_key"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("root config names %s (6.x has EchoVRCE's built in):\n%s", unwanted, s)
		}
	}
	for _, want := range []string{"discord_id: ${NEVR_DISCORD_ID:?", "password: ${NEVR_PASSWORD:?", "regions: [uwu, default]", "tick_rate_hz: 120"} {
		if !strings.Contains(s, want) {
			t.Errorf("root config lacks %q:\n%s", want, s)
		}
	}
	if _, err := Parse(got); err != nil {
		t.Error(err)
	}
	// With a password the Discord ID is part of the credential.
	r.Identity.DiscordID = ""
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "identity.discord_id") {
		t.Errorf("password without a Discord ID: got %v", err)
	}
	// An override for another backend is kept.
	o := RootDefault()
	o.Services.SocketURI = "ws://other:80/spr"
	if err := o.SetMatchmaking([]string{"x"}, nil); err != nil {
		t.Fatal(err)
	}
	b, err := o.Render()
	if err != nil || !strings.Contains(string(b), "socket_uri: ws://other:80/spr?regions=x") {
		t.Errorf("override lost: %s %v", b, err)
	}
}

// From nevr-runtime v6.9.0 a server without auth.password signs in by device
// code: its file names neither the password nor the Discord ID. A bare
// ${NEVR_PASSWORD} would stay literal when unset and be sent as the password.
func TestRootDeviceCode(t *testing.T) {
	r := RootDefault()
	r.Auth.Password, r.Identity.DiscordID = "", ""
	if err := r.SetMatchmaking([]string{"euw"}, []string{"123"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, unwanted := range []string{"password", "discord_id", "identity", "NEVR_PASSWORD", "auth:"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("device-code config names %s:\n%s", unwanted, s)
		}
	}
	for _, want := range []string{"regions: [euw]", "guilds: [\"123\"]", "tick_rate_hz: 120"} {
		if !strings.Contains(s, want) {
			t.Errorf("device-code config lacks %q:\n%s", want, s)
		}
	}
	if _, err := Parse(got); err != nil {
		t.Error(err)
	}
	// A Discord ID alone is fine: the runtime ignores it without a password.
	r.Identity.DiscordID = EnvRef(EnvDiscordID)
	if err := r.Validate(); err != nil {
		t.Errorf("device-code config with a Discord ID: %v", err)
	}
}

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

func TestRenderPlugins(t *testing.T) {
	r := RootDefault()
	r.Auth.Password, r.Identity.DiscordID = "", ""
	r.Plugins = []Plugin{
		{Name: "NvrContentOverlay", File: "NvrContentOverlay.dll", Early: true, Args: map[string]string{"active": "echo-combat"}},
		{Name: "NvrServerPatches", File: "NvrServerPatches.dll"},
	}
	got, err := r.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{"plugins:", "- name: NvrContentOverlay", "file: NvrContentOverlay.dll", "early: true", "active: echo-combat", "- name: NvrServerPatches"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, s)
		}
	}
	back, err := Parse(got)
	if err != nil || len(back.Plugins) != 2 || back.Plugins[0].Args["active"] != "echo-combat" {
		t.Errorf("round trip: %+v %v", back.Plugins, err)
	}
	for name, p := range map[string]Plugin{
		"no name":   {File: "a.dll"},
		"path":      {Name: "a", File: "x/a.dll"},
		"not a dll": {Name: "a", File: "a.exe"},
		"env":       {Name: "a", File: "a.dll", Args: map[string]string{"k": "${HOME}"}},
	} {
		c := RootDefault()
		c.Auth.Password, c.Identity.DiscordID = "", ""
		c.Plugins = []Plugin{p}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	c := RootDefault()
	c.Auth.Password, c.Identity.DiscordID = "", ""
	c.Plugins = []Plugin{{Name: "A", File: "a.dll"}, {Name: "A2", File: "A.dll"}}
	if err := c.Validate(); err == nil {
		t.Error("the same file twice: expected an error")
	}
}

func TestWithPlugins(t *testing.T) {
	own := []byte("version: 1\nnetwork:\n  tick_rate_hz: 60\nx-notes: mine\n")
	ps := []Plugin{{Name: "NvrServerPatches", File: "NvrServerPatches.dll", Args: map[string]string{"teamsize": "6"}}}
	got, err := WithPlugins(own, ps)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(got)
	if err != nil || c.Network.TickRateHz != 60 || len(c.Plugins) != 1 || c.Plugins[0].Args["teamsize"] != "6" || !strings.Contains(string(got), "x-notes: mine") {
		t.Errorf("%s %v", got, err)
	}
	if same, _ := WithPlugins(own, nil); string(same) != string(own) {
		t.Error("no plugins changed the file")
	}
	if _, err := WithPlugins([]byte("plugins: []\n"), ps); err != ErrOwnPlugins {
		t.Errorf("own plugins list: got %v", err)
	}
	if _, err := WithPlugins([]byte("{version: 1}"), ps); err == nil {
		t.Error("a flow mapping: expected an error")
	}
}

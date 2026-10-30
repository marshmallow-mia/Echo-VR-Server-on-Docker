package main

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	dir := t.TempDir()
	base := map[string]string{"EVR_PORT": "6792", "EVR_HOME": dir, "EVR_GAME_DIR": dir, "EVR_CONFIG_YAML": "",
		"NEVR_PASSWORD": "", "NEVR_DISCORD_ID": "", "NEVR_SOCKET_URI": "", "NEVR_SERVERDB_URI": "", "NEVR_HTTP_URI": ""}
	for k, v := range kv {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

// Without a password the server starts: a 6.9.0+ runtime signs in by a code
// to approve, so its file names neither the password nor the Discord ID; a
// 5.x runtime gets a reason instead of a config.
func TestOptionsWithoutPassword(t *testing.T) {
	setEnv(t, map[string]string{"NEVR_DISCORD_ID": "123456789012345678"})
	o, err := options()
	if err != nil {
		t.Fatal(err)
	}
	if !o.DeviceCode || o.DeviceLinked {
		t.Errorf("DeviceCode %v DeviceLinked %v", o.DeviceCode, o.DeviceLinked)
	}
	root := string(o.ConfigYAMLRoot)
	if o.ConfigYAMLRoot == nil || strings.Contains(root, "password") || strings.Contains(root, "discord_id") {
		t.Errorf("root config %q (%s)", root, o.RootConfigErr)
	}
	if o.ConfigYAML != nil || !strings.Contains(o.ConfigErr, "NEVR_PASSWORD is not set") {
		t.Errorf("5.x config %q err %q", o.ConfigYAML, o.ConfigErr)
	}
}

func TestOptionsWithPassword(t *testing.T) {
	setEnv(t, map[string]string{"NEVR_DISCORD_ID": "123456789012345678", "NEVR_PASSWORD": "x"})
	o, err := options()
	if err != nil {
		t.Fatal(err)
	}
	if o.DeviceCode {
		t.Error("DeviceCode with a password")
	}
	for _, b := range []string{string(o.ConfigYAMLRoot), string(o.ConfigYAML)} {
		if !strings.Contains(b, "password: ${NEVR_PASSWORD:?") || !strings.Contains(b, "discord_id: ${NEVR_DISCORD_ID:?") {
			t.Errorf("config lacks the credential:\n%s", b)
		}
		if strings.Contains(b, ": x") {
			t.Error("password value written to the file")
		}
	}
	// A password without the Discord ID cannot sign in.
	setEnv(t, map[string]string{"NEVR_PASSWORD": "x"})
	if o, _ = options(); o.ConfigYAMLRoot != nil || !strings.Contains(o.RootConfigErr, "identity.discord_id") {
		t.Errorf("root config without Discord ID: %q %q", o.ConfigYAMLRoot, o.RootConfigErr)
	}
}

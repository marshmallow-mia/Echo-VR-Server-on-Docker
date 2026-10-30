package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenPrefix is what the template up to v2.1.0 left: marked as set up by
// Wine, without the registry Wine's setup writes.
func brokenPrefix(t *testing.T, s *Supervisor) {
	t.Helper()
	os.MkdirAll(s.prefix(), 0o755)
	os.WriteFile(filepath.Join(s.prefix(), ".update-timestamp"), []byte("1790983071\n"), 0o644)
	os.WriteFile(filepath.Join(s.prefix(), ".evr-seeded"), nil, 0o644)
	os.WriteFile(filepath.Join(s.prefix(), "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644)
}

// fakeWineboot writes the registry key when called as "wine wineboot -u".
func fakeWineboot(t *testing.T, o *Options, writes bool) string {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls")
	body := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n"
	if writes {
		body += `[ "$1 $2" = "wineboot -u" ] && printf '%s\n' '[Software\\Microsoft\\Cryptography\\OID\\EncodingType 0] 1' >> "$WINEPREFIX/system.reg"` + "\n"
	}
	os.WriteFile(o.Wine, []byte(body+"exit 0\n"), 0o755)
	return calls
}

func TestRepairPrefixRunsWineSetupOnce(t *testing.T) {
	o, out := testOptions(t, "v5.4.2-ok.log", 6792)
	calls := fakeWineboot(t, &o, true)
	s := newSup(t, o)
	brokenPrefix(t, s)
	if prefixComplete(s.prefix()) {
		t.Fatal("broken prefix counts as complete")
	}
	if err := s.seedPrefix(); err != nil {
		t.Fatal(err)
	}
	if !prefixComplete(s.prefix()) {
		t.Fatal("prefix not repaired")
	}
	if err := s.seedPrefix(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if got := strings.TrimSpace(string(b)); got != "wineboot -u" {
		t.Errorf("wine calls %q, want one wineboot -u", got)
	}
	if !strings.Contains(out.String(), "the Wine prefix has no registry") {
		t.Errorf("console %q", out.String())
	}
}

func TestRepairPrefixFailsWithoutRegistry(t *testing.T) {
	o, _ := testOptions(t, "v5.4.2-ok.log", 6792)
	fakeWineboot(t, &o, false)
	s := newSup(t, o)
	brokenPrefix(t, s)
	if err := s.seedPrefix(); err == nil || !strings.Contains(err.Error(), "did not write the registry") {
		t.Fatalf("err %v", err)
	}
}

func TestRepairPrefixLeavesOthersAlone(t *testing.T) {
	o, _ := testOptions(t, "v5.4.2-ok.log", 6792)
	calls := fakeWineboot(t, &o, true)
	s := newSup(t, o)
	// No .update-timestamp: Wine sets the prefix up itself on first start.
	if err := s.seedPrefix(); err != nil {
		t.Fatal(err)
	}
	// A complete prefix.
	brokenPrefix(t, s)
	os.WriteFile(filepath.Join(s.prefix(), "system.reg"), []byte(`[Software\\Microsoft\\Cryptography\\OID\\x] 1`+"\n"), 0o644)
	if err := s.seedPrefix(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		b, _ := os.ReadFile(calls)
		t.Errorf("wine called: %q", b)
	}
}

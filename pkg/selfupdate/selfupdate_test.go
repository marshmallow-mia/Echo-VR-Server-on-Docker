package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/ghrel"
)

func fakeGitHub(t *testing.T, body string, sum string) *httptest.Server {
	t.Helper()
	asset := "evr-x_linux_" + runtime.GOARCH
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases":
			fmt.Fprintf(w, `[{"tag_name":"v2.0.0","prerelease":false,"assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"checksums.txt","browser_download_url":"%s/sums"}]},
				{"tag_name":"v2.1.0-rc.1","prerelease":true,"assets":[{"name":%q,"browser_download_url":"%s/bin"}]},
				{"tag_name":"v1.0.0","prerelease":false,"assets":[]}]`, asset, srv.URL, srv.URL, asset, srv.URL)
		case "/bin":
			w.Write([]byte(body))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", sum, asset)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPickAndApply(t *testing.T) {
	body := "#!/bin/sh\necho new\n"
	h := sha256.Sum256([]byte(body))
	srv := fakeGitHub(t, body, hex.EncodeToString(h[:]))
	exe := filepath.Join(t.TempDir(), "evr-x")
	os.WriteFile(exe, []byte("old"), 0o755)
	u := &Updater{Repo: "o/r", Binary: "evr-x", Current: "2.0.0", Exe: exe, Client: &ghrel.Client{BaseURL: srv.URL}}
	rels, err := u.Releases(context.Background())
	if err != nil || len(rels) != 2 || rels[0].Tag != "v2.1.0-rc.1" {
		t.Fatalf("releases %v %v", rels, err)
	}
	// A stable user stays on stable releases; a pre-release user follows pre-releases.
	if r, _ := Pick(rels, "2.0.0", ""); r.Tag != "v2.0.0" {
		t.Errorf("stable pick %s", r.Tag)
	}
	if r, _ := Pick(rels, "2.0.0-rc.4", ""); r.Tag != "v2.1.0-rc.1" {
		t.Errorf("pre pick %s", r.Tag)
	}
	if !Newer("2.0.0-rc.4", "v2.0.0") || Newer("2.0.0", "v2.0.0") || !Newer("dev", "v1.0.0") {
		t.Error("Newer")
	}
	if err := u.Apply(context.Background(), rels[1], func(string) {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != body {
		t.Errorf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".prev"); string(b) != "old" {
		t.Errorf("prev = %q", b)
	}
}

func TestChecksumMismatchKeepsBinary(t *testing.T) {
	srv := fakeGitHub(t, "evil", strings.Repeat("0", 64))
	exe := filepath.Join(t.TempDir(), "evr-x")
	os.WriteFile(exe, []byte("old"), 0o755)
	u := &Updater{Repo: "o/r", Binary: "evr-x", Exe: exe, Client: &ghrel.Client{BaseURL: srv.URL}}
	rels, _ := u.Releases(context.Background())
	if err := u.Apply(context.Background(), rels[1], func(string) {}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Error("binary replaced despite mismatch")
	}
	// A release without any checksum is refused too.
	if err := u.Apply(context.Background(), rels[0], func(string) {}); err == nil {
		t.Error("installed without a checksum")
	}
}

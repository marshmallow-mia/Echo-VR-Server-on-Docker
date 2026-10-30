package nevr

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serverZip(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"BugSplat64.dll": "dll " + tag, "echovr_server.exe": "exe", "README.md": "readme", "junk.txt": "x"} {
		w, _ := zw.Create("nevr-runtime-" + tag + "/" + name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// rootZip is a two-file release as nevr-runtime publishes it from v6.0.0 on:
// flat, with SHA256SUMS. badSum makes the runtime's SHA256SUMS line wrong.
func rootZip(t *testing.T, tag string, badSum bool) []byte {
	t.Helper()
	files := map[string]string{"BugSplat64.dll": "host " + tag, "nevr-runtime.dll": "runtime " + tag, "echovr_server.exe": "exe",
		"README.txt": "readme", "install.ps1": "ps1", "nevr-install.exe": "installer"}
	var sums strings.Builder
	for name, body := range files {
		sum := sha256.Sum256([]byte(body))
		hexSum := hex.EncodeToString(sum[:])
		if badSum && name == "nevr-runtime.dll" {
			hexSum = strings.Repeat("1", 64)
		}
		fmt.Fprintf(&sums, "%s  %s\n", hexSum, name)
	}
	files["SHA256SUMS"] = sums.String()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

func fakeGitHub(t *testing.T, badDigest string) *httptest.Server {
	return fakeGitHubSums(t, badDigest, false)
}

// fakeGitHubSums also lists v6.6.0 (two-file, -windows.zip only) and v5.10.0
// (-windows.zip only, before the root layout).
func fakeGitHubSums(t *testing.T, badDigest string, badSums bool) *httptest.Server {
	t.Helper()
	zips := map[string][]byte{}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/EchoTools/nevr-runtime/releases", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, tag := range []string{"v6.6.0", "v5.10.0", "v5.4.3", "v5.4.2", "v5.4.1", "v5.4.0", "v5.2.0", "v3.2.0"} {
			rel := map[string]any{"tag_name": tag, "prerelease": tag != "v3.2.0", "published_at": "2026-10-20T14:46:32Z"}
			var assets []map[string]any
			if tag == "v6.6.0" || tag == "v5.10.0" {
				z := rootZip(t, tag, badSums)
				zips[tag] = z
				sum := sha256.Sum256(z)
				assets = append(assets, map[string]any{"name": "nevr-runtime-" + tag + "-windows.zip", "size": len(z),
					"digest": "sha256:" + hex.EncodeToString(sum[:]), "browser_download_url": srv.URL + "/dl/" + tag})
			} else if tag != "v5.4.0" {
				z := serverZip(t, tag)
				zips[tag] = z
				sum := sha256.Sum256(z)
				digest := "sha256:" + hex.EncodeToString(sum[:])
				if tag == badDigest {
					digest = "sha256:" + strings.Repeat("0", 64)
				}
				assets = append(assets, map[string]any{"name": "nevr-runtime-" + tag + ".zip", "size": len(z), "digest": digest,
					"browser_download_url": srv.URL + "/dl/" + tag})
				assets = append(assets, map[string]any{"name": "nevr-runtime-" + tag + "-windows.zip", "browser_download_url": srv.URL + "/nope"})
			}
			rel["assets"] = assets
			out = append(out, rel)
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(zips[strings.TrimPrefix(r.URL.Path, "/dl/")])
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestListFiltersAndOrders(t *testing.T) {
	srv := fakeGitHub(t, "")
	c := &Client{BaseURL: srv.URL}
	rels, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rels {
		got = append(got, fmt.Sprintf("%s:%t", r.Tag, r.Blocked == ""))
	}
	// v5.4.1 and older are below MinVersion and left out entirely; v5.10.0 has
	// no package of the old name and comes before the root layout.
	if want := "v6.6.0:true v5.10.0:false v5.4.3:true v5.4.2:true"; strings.Join(got, " ") != want {
		t.Errorf("got %v want %s", got, want)
	}
	if rels[0].Asset.Name != "nevr-runtime-v6.6.0-windows.zip" {
		t.Errorf("v6.6.0 asset %q", rels[0].Asset.Name)
	}
	if rels[2].Asset.Name != "nevr-runtime-v5.4.3.zip" {
		t.Errorf("v5.4.3 must keep its server zip, got %q", rels[2].Asset.Name)
	}
	newest, err := c.Newest(context.Background(), true)
	if err != nil || newest.Tag != "v6.6.0" {
		t.Errorf("newest %v %v", newest.Tag, err)
	}
	if _, err := c.Newest(context.Background(), false); err == nil {
		t.Error("stable channel must not offer pre-releases")
	}
}

func TestStoreInstallUseRollback(t *testing.T) {
	srv := fakeGitHub(t, "")
	c := &Client{BaseURL: srv.URL}
	s := Store{Dir: filepath.Join(t.TempDir(), "nevr")}
	ctx := context.Background()
	for _, tag := range []string{"v5.4.2", "v5.4.3"} {
		rel, err := c.Find(ctx, tag)
		if err != nil {
			t.Fatal(err)
		}
		inst, err := s.Install(ctx, rel)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(s.Dir, tag, DLLName)); string(b) != "dll "+tag {
			t.Errorf("%s dll = %q", tag, b)
		}
		if _, err := os.Stat(filepath.Join(s.Dir, tag, "junk.txt")); !os.IsNotExist(err) {
			t.Error("unexpected file extracted")
		}
		if inst.DLLSHA256 == "" {
			t.Error("no dll checksum")
		}
	}
	if err := s.Use("v5.4.2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("v5.4.3"); err != nil {
		t.Fatal(err)
	}
	if s.Current() != "v5.4.3" || s.Previous() != "v5.4.2" {
		t.Fatalf("current=%s previous=%s", s.Current(), s.Previous())
	}
	if err := s.Remove("v5.4.2"); err == nil {
		t.Error("removed the rollback target")
	}
	if tag, err := s.Rollback(); err != nil || tag != "v5.4.2" || s.Current() != "v5.4.2" {
		t.Fatalf("rollback %s %v current=%s", tag, err, s.Current())
	}
	if err := s.MarkBad("v5.4.3", "registration failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.Use("v5.4.3"); err == nil {
		t.Error("used a runtime marked bad")
	}
	list, _ := s.List()
	if len(list) != 2 || list[0].Tag != "v5.4.3" || list[0].Bad == "" {
		t.Errorf("list %+v", list)
	}
	// The DLL resolves through the relative symlink.
	if b, err := os.ReadFile(filepath.Join(s.Dir, CurrentLink, DLLName)); err != nil || string(b) != "dll v5.4.2" {
		t.Errorf("current dll %q %v", b, err)
	}
}

func TestStoreInstallRootLayout(t *testing.T) {
	srv := fakeGitHub(t, "")
	rel, err := (&Client{BaseURL: srv.URL}).Find(context.Background(), "v6.6.0")
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: t.TempDir()}
	inst, err := s.Install(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Dir, "v6.6.0")
	if b, _ := os.ReadFile(filepath.Join(dir, RuntimeName)); string(b) != "runtime v6.6.0" {
		t.Errorf("runtime = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, DLLName)); string(b) != "host v6.6.0" {
		t.Errorf("host = %q", b)
	}
	want := sha256.Sum256([]byte("runtime v6.6.0"))
	if inst.RuntimeSHA256 != hex.EncodeToString(want[:]) {
		t.Errorf("runtime sha %q", inst.RuntimeSHA256)
	}
	for _, name := range []string{"install.ps1", "nevr-install.exe", "SHA256SUMS"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s extracted", name)
		}
	}
	got, _ := s.Get("v6.6.0")
	if got.RuntimeSHA256 != inst.RuntimeSHA256 {
		t.Errorf("manifest runtime sha %q", got.RuntimeSHA256)
	}
}

func TestInstallRejectsSumsMismatch(t *testing.T) {
	srv := fakeGitHubSums(t, "", true)
	rel, err := (&Client{BaseURL: srv.URL}).Find(context.Background(), "v6.6.0")
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: t.TempDir()}
	if _, err := s.Install(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("want SHA256SUMS error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "v6.6.0")); !os.IsNotExist(err) {
		t.Error("partial install left behind")
	}
}

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	srv := fakeGitHub(t, "v5.4.2")
	rel, err := (&Client{BaseURL: srv.URL}).Find(context.Background(), "v5.4.2")
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Dir: t.TempDir()}
	if _, err := s.Install(context.Background(), rel); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "v5.4.2")); !os.IsNotExist(err) {
		t.Error("partial install left behind")
	}
}

func TestVersionOrder(t *testing.T) {
	order := []string{"v5.4.2", "v5.4.10", "v6.0.0-rc.1", "v6.0.0"}
	for i := 0; i+1 < len(order); i++ {
		a, b := MustParseVersion(order[i]), MustParseVersion(order[i+1])
		if !a.Less(b) || b.Less(a) {
			t.Errorf("%s should sort before %s", a, b)
		}
	}
}

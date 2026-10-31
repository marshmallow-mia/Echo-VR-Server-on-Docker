package mods

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionRanges(t *testing.T) {
	cases := []struct {
		v, r string
		pre  bool
		want bool
	}{
		{"1.2.0", ">=1.0.0", false, true},
		{"1.2.0", ">=1.0.0 <1.2.0", false, false},
		{"1.2.0", "^1.0.0", false, true},
		{"2.0.0", "^1.0.0", false, false},
		{"0.3.5", "^0.3.0", false, true},
		{"0.4.0", "^0.3.0", false, false},
		{"1.2.9", "~1.2.0", false, true},
		{"1.3.0", "~1.2.0", false, false},
		{"6.9.0", ">=6.0.0", false, true},
		{"1.0.0-beta.1", ">=0.9.0", false, false},
		{"1.0.0-beta.1", ">=0.9.0", true, true},
		{"1.0.0-beta.2", ">=1.0.0-beta.1", false, true},
		{"1.0.0", "*", false, true},
		{"x", "*", false, false},
	}
	for _, c := range cases {
		if got := InRange(c.v, c.r, c.pre); got != c.want {
			t.Errorf("InRange(%q, %q, %v) = %v", c.v, c.r, c.pre, got)
		}
	}
	if !newer("1.0.0", "1.0.0-rc.10") || !newer("1.0.0-rc.10", "1.0.0-rc.2") || newer("1.0.0+a", "1.0.0+b") {
		t.Error("ordering")
	}
}

// feed is a fake release.echovr.de: files by path, the index signed with key.
type feed struct {
	files map[string][]byte
	key   ed25519.PrivateKey
	hits  map[string]int
}

func newFeed(t *testing.T) (*feed, *httptest.Server, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{7}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	f := &feed{files: map[string][]byte{}, key: priv, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		f.hits[p]++
		b, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return f, srv, pub
}

// sign writes dir's index.json over the listed paths, and its signature.
func (f *feed) sign(dir string, paths ...string) {
	files := map[string]string{}
	for _, p := range paths {
		files[p] = sha256Hex(f.files[dir+p])
	}
	idx, _ := json.Marshal(map[string]any{"serial": 1, "files": files})
	f.files[dir+"index.json"] = idx
	pub := f.key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(f.key, idx)
	sigs, _ := json.Marshal(map[string]any{"signatures": []map[string]string{{
		"key": sha256Hex(pub)[:16], "sha256": sha256Hex(idx), "sig": base64.StdEncoding.EncodeToString(sig),
	}}})
	f.files[dir+"index.json.sig"] = sigs
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sums(files map[string][]byte) []byte {
	var b strings.Builder
	for name, data := range files {
		fmt.Fprintf(&b, "%s  %s\n", sha256Hex(data), name)
	}
	return []byte(b.String())
}

// published sets up mods.json, catalogue.json and rollback.json with a loose
// DLL, a release in release-signer's shape, a pack and its overlay, a mod for
// players' games only, and a blocked version.
func published(t *testing.T, f *feed, base string) {
	t.Helper()
	dll := func(name string) []byte { return []byte("MZ " + name) }
	f.files["launcher/mods/NvrServerPatches-0.4.1.dll"] = dll("patches")
	f.files["launcher/mods/NvrContentOverlay-1.1.0.dll"] = dll("overlay")
	f.files["launcher/mods/NvrGpuRating-1.0.0.dll"] = dll("gpu")
	pack := zipOf(t, map[string][]byte{
		"plugins/EchoCombat.dll":         dll("combat"),
		"plugins/EchoCombat.ini":         []byte("[x]"),
		"content/combat/content.json":    []byte("{}"),
		"content/combat/maps/0x1234.pkg": []byte("data"),
		"README.txt":                     []byte("hi"),
	})
	f.files["launcher/mods/echocombat-1.1.9.zip"] = pack
	mods := map[string]any{"schema": 1, "mods": []map[string]any{
		{"id": "asset-patches", "name": "Asset Patches", "version": "1.2.0", "file": "NvrAssetPatches.dll", "shipped": true, "required": true, "target": "both"},
		{"id": "gpu-rating", "name": "GPU Rating", "version": "1.0.0", "file": "NvrGpuRating.dll", "url": "launcher/mods/NvrGpuRating-1.0.0.dll",
			"sha256": sha256Hex(dll("gpu")), "required": true, "target": "client"},
		{"id": "server-patches", "name": "Server Patches", "version": "0.4.1", "file": "NvrServerPatches.dll",
			"url": "launcher/mods/NvrServerPatches-0.4.1.dll", "sha256": sha256Hex(dll("patches")), "target": "server",
			"args": map[string]any{"teamsize": 4, "bots": false}, "requires": map[string]any{"nevr": ">=6.0.0"}},
		{"id": "content-overlay", "name": "Content Overlay", "version": "1.1.0", "file": "NvrContentOverlay.dll",
			"url": "launcher/mods/NvrContentOverlay-1.1.0.dll", "sha256": sha256Hex(dll("overlay"))},
		{"id": "echocombat", "name": "EchoCombat", "version": "1.1.9", "pack": map[string]any{
			"url": "launcher/mods/echocombat-1.1.9.zip", "sha256": sha256Hex(pack), "content": "combat", "plugins": []string{"EchoCombat.dll"}}},
	}}
	f.files["launcher/mods.json"], _ = json.Marshal(mods)

	// A community mod's release in release-signer's shape, 1.0.0 and a blocked 1.1.0.
	rel := func(ver string) map[string]any {
		inner := map[string][]byte{"Rules.dll": dll("rules " + ver), "LICENSE": []byte("MIT")}
		inner["SHA256SUMS"] = sums(inner)
		z := zipOf(t, inner)
		pkg := "rules-v" + ver + "-windows.zip"
		man, _ := json.Marshal(map[string]string{"id": "rules", "version": ver, "file": "Rules.dll", "package": pkg})
		top := sums(map[string][]byte{pkg: z, "echo-mod.json": man})
		dir := "launcher/mods/rules/v" + ver + "/"
		f.files[dir+pkg], f.files[dir+"echo-mod.json"], f.files[dir+"SHA256SUMS"] = z, man, top
		return map[string]any{"id": "rules", "name": "Rules", "version": ver, "file": "Rules.dll", "target": "both",
			"release": map[string]any{"base": base + dir, "tag": "v" + ver, "assets": map[string]any{
				"package":  map[string]any{"name": pkg, "sha256": sha256Hex(z)},
				"manifest": map[string]any{"name": "echo-mod.json", "sha256": sha256Hex(man)},
				"sums":     map[string]any{"name": "SHA256SUMS", "sha256": sha256Hex(top)},
			}, "signer": map[string]any{"status": "unsigned"}}}
	}
	cat := map[string]any{"schema": 1, "mods": []map[string]any{{"id": "rules", "versions": []any{rel("1.0.0"), rel("1.1.0")}}}}
	f.files["launcher/catalogue.json"], _ = json.Marshal(cat)
	f.files["launcher/rollback.json"], _ = json.Marshal(map[string]any{"blocked": []map[string]any{
		{"part": "mod", "id": "rules", "versions": "1.1.0", "note": "crashes servers"}}})
	f.sign("", "launcher/mods.json", "launcher/catalogue.json", "launcher/rollback.json")
}

func fetchFrom(t *testing.T) (*feed, Source, Store) {
	f, srv, pub := newFeed(t)
	published(t, f, "")
	src := Source{Origin: srv.URL, Keys: [][]byte{pub}}
	st := Store{Dir: t.TempDir(), Mirrors: []string{srv.URL + "/"}}
	// The test server isn't a trusted host: the store takes the resolved URLs as given.
	return f, src, st
}

func TestFetchChecksTheSignedIndex(t *testing.T) {
	f, src, _ := fetchFrom(t)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"asset-patches", "gpu-rating", "server-patches", "content-overlay", "echocombat", "rules"} {
		if len(got.Catalog.Versions[id]) == 0 {
			t.Errorf("%s missing (skipped: %v)", id, got.Skipped)
		}
	}
	// A catalogue that isn't what the signed index lists is refused.
	f.files["launcher/mods.json"] = append(f.files["launcher/mods.json"], ' ')
	if _, err := src.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "signed index") {
		t.Fatalf("changed mods.json: %v", err)
	}
	// An index signed by another key is refused.
	f.sign("", "launcher/mods.json")
	other := Source{Origin: src.Origin, Keys: [][]byte{make([]byte, 32)}}
	if _, err := other.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("other key: %v", err)
	}
}

func TestResolveLikeTheLauncher(t *testing.T) {
	_, src, _ := fetchFrom(t)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := got.Catalog
	off := false
	specs := MergeSpecs(
		[]Spec{{ID: "server-patches"}, {ID: "rules"}, {ID: "echocombat"}, {ID: "gpu-rating"}},
		[]Spec{{ID: "server-patches", Args: map[string]string{"teamsize": "6"}}, {ID: "echocombat", Enabled: &off}},
	)
	lock, probs := Resolve(c, specs, "v6.9.0")
	ids := map[string]Locked{}
	for _, m := range lock.Mods {
		ids[m.ID] = m
	}
	if _, ok := ids["gpu-rating"]; ok {
		t.Error("a mod for players' games only is on a server")
	}
	if _, ok := ids["asset-patches"]; ok {
		t.Error("a shipped mod nobody listed is on a server")
	}
	if _, ok := ids["echocombat"]; ok {
		t.Error("a mod the server left out is on")
	}
	sp := ids["server-patches"]
	if sp.Args["teamsize"] != "6" || sp.Args["bots"] != "false" {
		t.Errorf("args: %v", sp.Args)
	}
	if ids["rules"].Version != "1.0.0" {
		t.Errorf("rules: %q (1.1.0 is blocked)", ids["rules"].Version)
	}
	if !strings.Contains(fmt.Sprint(probs), "only for players' games") {
		t.Errorf("problems: %v", probs)
	}
	// The wrong nEVR leaves it out.
	lock, probs = Resolve(c, []Spec{{ID: "server-patches"}}, "v5.4.3")
	if len(lock.Mods) != 0 || !strings.Contains(fmt.Sprint(probs), "needs nEVR >=6.0.0") {
		t.Errorf("5.4.3: %v %v", lock.Mods, probs)
	}
	// A pinned blocked version isn't run.
	if _, probs = Resolve(c, []Spec{{ID: "rules", Version: "1.1.0"}}, ""); !strings.Contains(fmt.Sprint(probs), "crashes servers") {
		t.Errorf("pinned blocked: %v", probs)
	}
	// A pack brings its overlay first, early, serving its content.
	lock, _ = Resolve(c, []Spec{{ID: "echocombat"}}, "v6.9.0")
	if len(lock.Mods) != 2 || lock.Mods[0].ID != "content-overlay" || !lock.Mods[0].Early || lock.Mods[0].Args["active"] != "combat" {
		t.Errorf("pack: %+v", lock.Mods)
	}
}

func TestStoreAndPlace(t *testing.T) {
	_, src, st := fetchFrom(t)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lock, probs := Resolve(got.Catalog, []Spec{{ID: "server-patches"}, {ID: "rules"}, {ID: "echocombat"}, {ID: "asset-patches"}}, "v6.9.0")
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	for _, m := range lock.Mods {
		if m.Shipped {
			continue
		}
		if _, err := st.Install(context.Background(), m.Entry); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	game := t.TempDir()
	os.WriteFile(filepath.Join(game, "NvrAssetPatches.dll"), []byte("MZ shipped"), 0o644)
	d := Dirs{Store: st.Dir, GamePlugins: game, Plugins: filepath.Join(root, "plugins"), Content: filepath.Join(root, "content"),
		Local: filepath.Join(root, "local"), Record: filepath.Join(root, "mods-placed.json")}
	os.WriteFile(filepath.Join(root, "foreign.txt"), nil, 0o644)
	placed, err := Place(lock, d)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, l := range placed.Lines {
		files = append(files, l.File)
	}
	want := "NvrContentOverlay.dll EchoCombat.dll NvrServerPatches.dll Rules.dll NvrAssetPatches.dll"
	if strings.Join(files, " ") != want {
		t.Errorf("lines %v, want %s", files, want)
	}
	for _, p := range []string{"plugins/Rules.dll", "plugins/EchoCombat.ini", "content/combat/content.json", "content/combat/maps/0x1234.pkg", "plugins/NvrAssetPatches.dll"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s not placed", p)
		}
	}
	// A file changed in the store is refused, not loaded.
	in, _ := st.Get("rules", "1.0.0")
	os.Chmod(st.Path(in, "plugins/Rules.dll"), 0o644)
	os.WriteFile(st.Path(in, "plugins/Rules.dll"), []byte("MZ evil"), 0o644)
	os.Remove(filepath.Join(d.Plugins, "Rules.dll"))
	if _, err := Place(lock, d); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("changed store file: %v", err)
	}
	// Without the pack, its files go; what others put there stays.
	lock2, _ := Resolve(got.Catalog, []Spec{{ID: "server-patches"}}, "v6.9.0")
	os.WriteFile(filepath.Join(d.Plugins, "Theirs.dll"), []byte("MZ"), 0o644)
	if _, err := Place(lock2, d); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"plugins/EchoCombat.dll", "content/combat/content.json", "content/combat", "plugins/Rules.dll"} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s stayed", gone)
		}
	}
	for _, stays := range []string{"plugins/Theirs.dll", "plugins/NvrServerPatches.dll", "foreign.txt"} {
		if _, err := os.Stat(filepath.Join(root, stays)); err != nil {
			t.Errorf("%s went", stays)
		}
	}
	// Local plugins only when on.
	os.MkdirAll(d.Local, 0o755)
	os.WriteFile(filepath.Join(d.Local, "Mine.dll"), []byte("MZ mine"), 0o644)
	p, _ := Place(lock2, d)
	if len(p.Lines) != 1 {
		t.Errorf("local plugins off: %v", p.Lines)
	}
	lock2.LocalPlugins = true
	if p, _ = Place(lock2, d); len(p.Lines) != 2 || p.Lines[1].File != "Mine.dll" {
		t.Errorf("local plugins on: %v", p.Lines)
	}
}

func TestStoreRefusesWhatDoesntCheck(t *testing.T) {
	f, src, st := fetchFrom(t)
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := got.Catalog.Choose("server-patches", "", false)
	f.files["launcher/mods/NvrServerPatches-0.4.1.dll"] = []byte("MZ other")
	if _, err := st.Install(context.Background(), sp); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("changed download: %v", err)
	}
	rules, _ := got.Catalog.Choose("rules", "", false)
	dir := "launcher/mods/rules/v1.0.0/"
	f.files[dir+"echo-mod.json"] = []byte(`{"id":"rules","version":"9.9.9","file":"Rules.dll","package":"rules-v1.0.0-windows.zip"}`)
	if _, err := st.Install(context.Background(), rules); err == nil {
		t.Fatal("a release whose manifest doesn't match its pin was installed")
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "rules")); err == nil {
		t.Error("a refused release left files")
	}
}

func TestDevFolderGoesOverPublished(t *testing.T) {
	f, src, _ := fetchFrom(t)
	code := "abcdefghijklmnopqrstuvwxyz"
	dir := "launcher/dev/" + code + "/"
	f.files[dir+"NvrServerPatches.dll"] = []byte("MZ dev")
	f.files[dir+"mods.json"], _ = json.Marshal(map[string]any{"mods": []map[string]any{
		{"id": "server-patches", "name": "Server Patches (dev)", "version": "0.5.0-dev.1", "file": "NvrServerPatches.dll",
			"url": "NvrServerPatches.dll", "sha256": sha256Hex([]byte("MZ dev")), "target": "server"},
		{"id": "escape", "name": "Escape", "version": "1.0.0", "file": "Escape.dll",
			"url": "https://release.echovr.de/launcher/mods/x.dll", "sha256": strings.Repeat("a", 64)},
	}})
	f.sign(dir, "mods.json")
	src.DevCodes = []string{"ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ"}
	got, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vs := got.Catalog.Versions["server-patches"]
	if len(vs) != 1 || vs[0].Version != "0.5.0-dev.1" || !strings.HasPrefix(vs[0].Source, "dev:") {
		t.Errorf("dev entry: %+v", vs)
	}
	if _, ok := got.Catalog.Versions["escape"]; ok || !strings.Contains(fmt.Sprint(got.Skipped), "dev folder") {
		t.Errorf("a dev entry downloading from outside its folder: %v", got.Skipped)
	}
	// A dev version is a prerelease: it runs only when asked for, or pinned.
	if _, err := got.Catalog.Choose("server-patches", "", false); err == nil {
		t.Error("a dev prerelease ran unasked")
	}
	if e, err := got.Catalog.Choose("server-patches", "", true); err != nil || e.Version != "0.5.0-dev.1" {
		t.Errorf("prerelease: %v %v", e.Version, err)
	}
}

func TestTargetAndPluginFile(t *testing.T) {
	if !Target("").OnServers() || !Target("weird").OnServers() || Target("client").OnServers() || !Target("server").OnServers() {
		t.Error("targets")
	}
	if PluginFile("BugSplat64.dll") || PluginFile("../x.dll") || !PluginFile("Rules.dll") {
		t.Error("plugin files")
	}
}

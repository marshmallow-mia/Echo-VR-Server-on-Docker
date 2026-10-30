package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuf) Write(p []byte) (int, error) { w.mu.Lock(); defer w.mu.Unlock(); return w.b.Write(p) }
func (w *syncBuf) String() string              { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

// testOptions builds a fake game tree, runtime and a "wine" that replays a
// fixture and then hangs like nevr-runtime #723.
func testOptions(t *testing.T, fixture string, port int) (Options, *syncBuf) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("game/bin/win10/echovr.exe", "x", 0o644)
	write("game/content/a", "x", 0o644)
	write("game/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json", `{"port": 6792, "retries": 10}`, 0o644)
	write("nevr/v5.4.2/BugSplat64.dll", "dll", 0o644)
	os.Symlink("v5.4.2", filepath.Join(dir, "nevr/current"))
	fx, _ := filepath.Abs("testdata/" + fixture)
	write("bin/wine", "#!/bin/sh\ncat '"+fx+"' >&2\nexec sleep 300\n", 0o755)
	out := &syncBuf{}
	return Options{
		Instance: "test", GameDir: filepath.Join(dir, "game"), NevrDir: filepath.Join(dir, "nevr"),
		Home: filepath.Join(dir, "home"), Port: port, HTTPPort: port + 100, PublisherLock: "echovrce",
		Wine: filepath.Join(dir, "bin/wine"), Console: out,
		RegisterTimeout: 5 * time.Second, TeardownGrace: 200 * time.Millisecond, ShutdownGrace: 200 * time.Millisecond,
	}, out
}

func newSup(t *testing.T, o Options) *Supervisor {
	t.Helper()
	os.MkdirAll(o.Home, 0o755)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunOnceRegisteredThenHang(t *testing.T) {
	o, out := testOptions(t, "registered-sigint.jsonl", 6795)
	s := newSup(t, o)
	start := time.Now()
	res := s.runOnce(context.Background())
	if !res.clean || !res.registered || res.reason != ReasonNone {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("hung server not killed promptly: %s", d)
	}
	st, err := status.Read(filepath.Join(o.Home, status.FileName))
	if err != nil || st.PublicIP != "168.119.2.94" || st.Registrations != 1 || st.RuntimeVersion != "5.4.2" {
		t.Errorf("status %+v %v", st, err)
	}
	if !strings.Contains(out.String(), ReadyMarker) {
		t.Errorf("no ready marker in console output")
	}
	if strings.Contains(out.String(), "[NEVR.SOCIAL]") {
		t.Errorf("noisy lines not filtered")
	}
	nc, _ := os.ReadFile(filepath.Join(o.Home, "root/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json"))
	if !strings.Contains(string(nc), `"port": 6795`) {
		t.Errorf("netconfig: %s", nc)
	}
}

func TestRunOncePortMismatch(t *testing.T) {
	o, _ := testOptions(t, "registered-sigint.jsonl", 6796) // fixture binds 6795
	res := newSup(t, o).runOnce(context.Background())
	if res.clean || res.reason != ReasonInvalidConfig || !strings.Contains(res.detail, "6795") {
		t.Fatalf("result %+v", res)
	}
}

func TestRunOnceLoginRejected(t *testing.T) {
	o, _ := testOptions(t, "no-password.jsonl", 6795)
	res := newSup(t, o).runOnce(context.Background())
	if res.clean || res.reason != ReasonBadCredentials || res.reason.Retryable() {
		t.Fatalf("result %+v", res)
	}
}

func TestRunOnceRegistrationTimeout(t *testing.T) {
	o, _ := testOptions(t, "serverdb-unreachable.jsonl", 6799)
	o.RegisterTimeout = 500 * time.Millisecond
	res := newSup(t, o).runOnce(context.Background())
	if res.clean || res.reason != ReasonRegistration || !res.reason.Retryable() {
		t.Fatalf("result %+v", res)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	o, _ := testOptions(t, "serverdb-unreachable.jsonl", 6799)
	o.StopTimeout = 300 * time.Millisecond
	s := newSup(t, o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if st := s.Status(); st.State != status.StateStopped {
		t.Errorf("state %s", st.State)
	}
}

// A helper that inherits the output pipe (like wineserver) must not delay the
// stop, and the teardown line must shorten the Ctrl+C timeout.
func TestStopWithPipeHolder(t *testing.T) {
	o, out := testOptions(t, "registered-sigint.jsonl", 6795)
	fx, _ := filepath.Abs("testdata/registered-sigint.jsonl")
	head := filepath.Join(o.Home, "..", "head.jsonl")
	b, _ := os.ReadFile(fx)
	i := strings.Index(string(b), "CTRL+C teardown complete")
	j := strings.LastIndex(string(b[:i]), "\n")
	os.WriteFile(head, b[:j+1], 0o644)
	tail := filepath.Join(o.Home, "..", "tail.jsonl")
	os.WriteFile(tail, b[j+1:], 0o644)
	// On SIGINT print the teardown lines and hang; a background child keeps the pipe.
	script := "#!/bin/sh\n(sleep 300) &\ntrap \"cat '" + tail + "' >&2\" INT\ncat '" + head + "' >&2\nwhile :; do sleep 1; done\n"
	os.WriteFile(o.Wine, []byte(script), 0o755)
	o.StopTimeout = 20 * time.Second
	s := newSup(t, o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().State != status.StateRegistered && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("stop hung\n%s", out)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("stop took %s, want about the teardown grace", d)
	}
}

// twoFile makes "current" a 6.x runtime (host plus nevr-runtime.dll) and the
// fake wine print fixture, then exit with code.
func twoFile(t *testing.T, o *Options, fixture string, code int) {
	t.Helper()
	dir := filepath.Join(o.NevrDir, "v6.6.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "BugSplat64.dll"), []byte("host"), 0o644)
	os.WriteFile(filepath.Join(dir, game.RuntimeName), []byte("runtime"), 0o644)
	os.Remove(filepath.Join(o.NevrDir, "current"))
	if err := os.Symlink("v6.6.0", filepath.Join(o.NevrDir, "current")); err != nil {
		t.Fatal(err)
	}
	o.ConfigYAML = []byte("version: 1 # 5.x\n")
	o.ConfigYAMLRoot = []byte("version: 1 # root\n")
	o.User = "container"
	fx, _ := filepath.Abs("testdata/" + fixture)
	started := filepath.Join(filepath.Dir(o.Wine), "started")
	script := fmt.Sprintf("#!/bin/sh\ntouch '%s'\ncat '%s' >&2\nexit %d\n", started, fx, code)
	if err := os.WriteFile(o.Wine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceTwoFileRoot(t *testing.T) {
	o, out := testOptions(t, "v6.6.0-no-password.log", 6792)
	twoFile(t, &o, "v6.6.0-no-password.log", 1)
	legacy := filepath.Join(o.Home, "root/_local", game.CredentialsName)
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(legacy, []byte(`{"refresh_token":"old"}`), 0o600)
	os.WriteFile(filepath.Join(o.Home, "root/_local/config.yaml"), []byte("stale"), 0o640)
	s := newSup(t, o)
	res := s.runOnce(context.Background())
	if res.clean || res.reason != ReasonInvalidConfig || !strings.Contains(res.detail, "no credential configured") {
		t.Fatalf("result %+v\n%s", res, out)
	}
	root := game.NevrRoot(filepath.Join(o.Home, ".wine"), "container")
	for name, want := range map[string]string{game.RuntimeName: "runtime", "config.yaml": "version: 1 # root\n", game.CredentialsName: `{"refresh_token":"old"}`} {
		if b, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(b) != want {
			t.Errorf("root %s = %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(o.Home, "root/_local/config.yaml")); !os.IsNotExist(err) {
		t.Error("_local/config.yaml left for a two-file runtime")
	}
	if b, _ := os.Readlink(filepath.Join(o.Home, "root/bin/win10/BugSplat64.dll")); !strings.HasSuffix(b, "v6.6.0/BugSplat64.dll") {
		t.Errorf("host link %q", b)
	}
	if st := s.Status(); st.RuntimeVersion != "6.6.0" {
		t.Errorf("runtime version %q", st.RuntimeVersion)
	}
	// The runtime rotates the root's token; a later start must not put _local's back.
	os.WriteFile(filepath.Join(root, game.CredentialsName), []byte(`{"refresh_token":"rotated"}`), 0o600)
	s.runOnce(context.Background())
	if b, _ := os.ReadFile(filepath.Join(root, game.CredentialsName)); string(b) != `{"refresh_token":"rotated"}` {
		t.Errorf("root sign-in overwritten: %s", b)
	}
}

func TestRunOnceTwoFileNeedsRootConfig(t *testing.T) {
	o, _ := testOptions(t, "v6.6.0-no-password.log", 6792)
	twoFile(t, &o, "v6.6.0-no-password.log", 1)
	o.ConfigYAMLRoot, o.RootConfigErr = nil, "auth.password is required"
	res := newSup(t, o).runOnce(context.Background())
	if res.reason != ReasonInvalidConfig || !strings.Contains(res.detail, "auth.password is required") || !strings.Contains(res.detail, "v6.6.0") {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.Wine), "started")); !os.IsNotExist(err) {
		t.Error("game started without a usable config")
	}
}

func TestRunOnceRuntimeRefused(t *testing.T) {
	o, _ := testOptions(t, "v6.6.0-runtime-refused-wine10.log", 6792)
	twoFile(t, &o, "v6.6.0-runtime-refused-wine10.log", 78)
	res := newSup(t, o).runOnce(context.Background())
	if res.clean || res.reason != ReasonRuntimeRefused || res.reason.Retryable() || !strings.Contains(res.detail, "PKCS#7") || res.exitCode != 78 {
		t.Fatalf("result %+v", res)
	}
}

func TestRunOnceExitCodes(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.log")
	os.WriteFile(empty, nil, 0o644)
	for code, want := range map[int]Reason{78: ReasonRuntimeRefused, 64: ReasonInvalidConfig} {
		o, _ := testOptions(t, "v6.6.0-no-password.log", 6792)
		twoFile(t, &o, "v6.6.0-no-password.log", code)
		os.WriteFile(o.Wine, []byte(fmt.Sprintf("#!/bin/sh\nexit %d\n", code)), 0o755)
		res := newSup(t, o).runOnce(context.Background())
		if res.clean || res.reason != want || res.exitCode != code {
			t.Errorf("exit %d: result %+v", code, res)
		}
	}
}

// Exit 78 repeats on every start: the instance must go to failed, not backoff.
func TestRunRuntimeRefusedIsNotRetried(t *testing.T) {
	o, _ := testOptions(t, "v6.6.0-runtime-refused-wine10.log", 6792)
	twoFile(t, &o, "v6.6.0-runtime-refused-wine10.log", 78)
	s := newSup(t, o)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.Status().State != status.StateFailed && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := s.Status(); st.State != status.StateFailed || st.Reason != string(ReasonRuntimeRefused) {
		t.Fatalf("status %+v", st)
	}
	cancel()
	<-done
}

// twoFileAt is twoFile with the runtime tag.
func twoFileAt(t *testing.T, o *Options, tag, fixture string, code int) {
	t.Helper()
	twoFile(t, o, fixture, code)
	dir := filepath.Join(o.NevrDir, tag)
	if err := os.Rename(filepath.Join(o.NevrDir, "v6.6.0"), dir); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(o.NevrDir, "current"))
	if err := os.Symlink(tag, filepath.Join(o.NevrDir, "current")); err != nil {
		t.Fatal(err)
	}
}

// A server waiting for its sign-in code to be approved is not "unregistered":
// the register timer starts once the sign-in ends, and the console shows the
// link instead of the JSON prompt.
func TestRunOnceDeviceCodeWaitsForApproval(t *testing.T) {
	o, out := testOptions(t, "v6.9.0-device-code.log", 6792)
	twoFileAt(t, &o, "v6.9.0", "v6.9.0-device-code.log", 0)
	o.DeviceCode = true
	o.RegisterTimeout = 700 * time.Millisecond
	fx, _ := filepath.Abs("testdata/v6.9.0-device-code.log")
	// Up to the prompt, a wait longer than RegisterTimeout, then the rest.
	script := fmt.Sprintf("#!/bin/sh\nsed -n '1,115p' '%[1]s' >&2\nsleep 2\nsed -n '116,$p' '%[1]s' >&2\nexec sleep 300\n", fx)
	os.WriteFile(o.Wine, []byte(script), 0o755)
	var waiting status.Status
	o.OnEvent = func(ev Event, st status.Status) {
		if ev.Kind == KindSignInCode {
			waiting = st
		}
	}
	s := newSup(t, o)
	res := s.runOnce(context.Background())
	if !res.registered || res.reason != ReasonNone {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if waiting.State != status.StateAwaitingApproval || waiting.SignInURL != "https://echovrce.com/device?code=TEST" || waiting.SignInExpires.IsZero() {
		t.Errorf("status while waiting: %+v", waiting)
	}
	if st := s.Status(); st.SignInURL != "" || !st.SignInExpires.IsZero() {
		t.Errorf("sign-in link kept after the approval: %+v", st)
	}
	c := out.String()
	if !strings.Contains(c, "[evr] approve this server: open https://echovrce.com/device?code=TEST") {
		t.Errorf("no approval line:\n%s", c)
	}
	if strings.Contains(c, `"event":"server_sign_in_code"`) {
		t.Error("raw JSON prompt printed")
	}
}

// Before v6.9.0 a two-file runtime refuses a server without a password.
func TestRunOnceDeviceCodeNeedsV690(t *testing.T) {
	o, _ := testOptions(t, "v6.6.0-no-password.log", 6792)
	twoFile(t, &o, "v6.6.0-no-password.log", 1)
	o.DeviceCode = true
	res := newSup(t, o).runOnce(context.Background())
	if res.reason != ReasonInvalidConfig || !strings.Contains(res.detail, "v6.9.0") || !strings.Contains(res.detail, "NEVR_PASSWORD") {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.Wine), "started")); !os.IsNotExist(err) {
		t.Error("v6.6.0 started without a password")
	}
}

// A 5.x runtime without a usable config says why instead of starting.
func TestRunOnceOneFileNeedsConfig(t *testing.T) {
	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.ConfigYAML, o.ConfigErr = nil, "NEVR_PASSWORD is not set"
	res := newSup(t, o).runOnce(context.Background())
	if res.reason != ReasonInvalidConfig || !strings.Contains(res.detail, "NEVR_PASSWORD is not set") {
		t.Fatalf("result %+v", res)
	}
}

// After the approval the register timer runs again: a server that signs in
// but never registers is restarted.
func TestRunOnceDeviceCodeThenNoRegistration(t *testing.T) {
	o, out := testOptions(t, "v6.9.0-device-code.log", 6792)
	twoFileAt(t, &o, "v6.9.0", "v6.9.0-device-code.log", 0)
	o.DeviceCode = true
	o.RegisterTimeout = 500 * time.Millisecond
	fx, _ := filepath.Abs("testdata/v6.9.0-device-code.log")
	// The prompt and the approval (line 182), then nothing.
	os.WriteFile(o.Wine, []byte(fmt.Sprintf("#!/bin/sh\nsed -n '1,182p' '%s' >&2\nexec sleep 300\n", fx)), 0o755)
	start := time.Now()
	res := newSup(t, o).runOnce(context.Background())
	if res.registered || res.reason != ReasonRegistration {
		t.Fatalf("result %+v\n%s", res, out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("not restarted promptly after the approval: %s", d)
	}
}

package supervisor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

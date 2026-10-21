// Package supervisor runs one Echo VR dedicated server under Wine: it prepares
// the instance tree, starts the game, follows its log, restarts it after every
// match and works around the runtime's shutdown hang (nevr-runtime #723).
package supervisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/status"
)

// ReadyMarker starts the line printed once the server is registered; the
// Pterodactyl egg uses it as its "done" string.
const ReadyMarker = "[evr] READY"

// Options configure a supervisor. Zero durations take the defaults.
type Options struct {
	Instance string
	// GameDir is the shared game tree, NevrDir holds one directory per runtime
	// tag plus a "current" symlink. Runtime is a tag or "current".
	GameDir, NevrDir, Runtime string
	// Home is the instance's writable directory: farm root, prefix, status.
	Home string
	// PrefixTemplate is copied into the Wine prefix on first start.
	PrefixTemplate string
	Port, HTTPPort int
	PublisherLock  string
	ConfigYAML     []byte
	Wine           string   // default "wine"
	ExtraArgs      []string // appended to the game's command line
	Env            []string // added to the game's environment

	RegisterTimeout time.Duration // start → registered; default 180s
	TeardownGrace   time.Duration // after "CTRL+C teardown complete"; default 3s
	ShutdownGrace   time.Duration // after "Graceful shutdown …"; default 10s
	StopTimeout     time.Duration // Ctrl+C → hard kill; default 25s
	Heartbeat       time.Duration // status rewrite interval; default 15s

	// Console receives the supervisor's own lines and the game lines Filter keeps.
	Console io.Writer
	// Filter decides which game lines reach Console; nil uses DefaultFilter.
	Filter func(line string) bool
	// Commands, when set, is read for console commands (status, restart, help).
	Commands io.Reader
	// ControlFile, when set, is polled for one-shot commands written by host
	// tools ("restart"); it is removed after reading.
	ControlFile string
	// OnEvent is called for every parsed event (metrics, notifications).
	OnEvent func(Event, status.Status)
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&o.RegisterTimeout, 180*time.Second)
	set(&o.TeardownGrace, 3*time.Second)
	set(&o.ShutdownGrace, 10*time.Second)
	set(&o.StopTimeout, 25*time.Second)
	set(&o.Heartbeat, 15*time.Second)
	if o.Wine == "" {
		o.Wine = "wine"
	}
	if o.Runtime == "" {
		o.Runtime = "current"
	}
	if o.Console == nil {
		o.Console = os.Stdout
	}
	if o.Filter == nil {
		o.Filter = DefaultFilter
	}
}

// DefaultFilter hides the runtime's high-volume diagnostic lines.
func DefaultFilter(line string) bool {
	for _, noisy := range []string{"[NEVR.SOCIAL]", "server->game", "game->server", "hook_liveness", "[NEVR.LOGFILTER] health", "NSUSER] saved", "[NEVR.SELFCHECK]"} {
		if strings.Contains(line, noisy) {
			return false
		}
	}
	return true
}

// Supervisor runs the restart loop for one instance.
type Supervisor struct {
	o Options

	mu      sync.Mutex
	st      status.Status
	proc    *os.Process
	restart chan struct{}
}

// New validates opts and returns a supervisor.
func New(o Options) (*Supervisor, error) {
	o.defaults()
	if o.Instance == "" || o.GameDir == "" || o.NevrDir == "" || o.Home == "" {
		return nil, errors.New("supervisor: instance, game dir, nevr dir and home are required")
	}
	if o.Port == 0 {
		return nil, errors.New("supervisor: a game port is required")
	}
	return &Supervisor{
		o:       o,
		st:      status.Status{Instance: o.Instance, Port: o.Port, HTTPPort: o.HTTPPort, State: status.StateStopped},
		restart: make(chan struct{}, 1),
	}, nil
}

// Paths inside Home.
func (s *Supervisor) root() string       { return filepath.Join(s.o.Home, "root") }
func (s *Supervisor) prefix() string     { return filepath.Join(s.o.Home, ".wine") }
func (s *Supervisor) statusPath() string { return filepath.Join(s.o.Home, status.FileName) }

// Status returns a copy of the current status.
func (s *Supervisor) Status() status.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *Supervisor) update(f func(*status.Status)) {
	s.mu.Lock()
	prev := s.st.State
	f(&s.st)
	if s.st.State != prev {
		s.st.Since = time.Now().UTC()
	}
	st := s.st
	s.mu.Unlock()
	if err := status.Write(s.statusPath(), st); err != nil {
		s.logf("cannot write status: %v", err)
	}
}

func (s *Supervisor) logf(format string, args ...any) {
	fmt.Fprintf(s.o.Console, "[evr] "+format+"\n", args...)
}

// Run blocks until ctx is cancelled, restarting the game as needed.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.o.Commands != nil {
		go s.readCommands(ctx)
	}
	if s.o.ControlFile != "" {
		go s.watchControl(ctx)
	}
	go s.heartbeat(ctx)
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		res := s.runOnce(ctx)
		if ctx.Err() != nil {
			break
		}
		code := res.exitCode
		s.update(func(st *status.Status) { st.LastExitCode = &code; st.PID = 0; st.Session = "" })
		var wait time.Duration
		switch {
		case res.clean:
			s.logf("game process ended (code %d); starting a new one", code)
			backoff = 5 * time.Second
		case res.reason != ReasonNone && !res.reason.Retryable():
			wait = 10 * time.Minute
			s.logf("server stopped: %s — %s. Fix the settings and restart; retrying in %s", res.reason, res.detail, wait)
			s.update(func(st *status.Status) {
				st.State, st.Reason, st.LastError, st.NextStart = status.StateFailed, string(res.reason), res.detail, time.Now().Add(wait).UTC()
			})
		default:
			wait = backoff
			backoff = min(backoff*2, 5*time.Minute)
			s.logf("server exited (code %d, %s); restarting in %s", code, orDefault(string(res.reason), "unexpected exit"), wait)
			s.update(func(st *status.Status) {
				st.State, st.Reason, st.LastError, st.NextStart = status.StateBackoff, string(res.reason), res.detail, time.Now().Add(wait).UTC()
			})
		}
		if res.registered {
			backoff = 5 * time.Second
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			case <-s.restart:
			}
		}
		s.update(func(st *status.Status) { st.Restarts++ })
	}
	s.update(func(st *status.Status) { st.State, st.PID, st.NextStart = status.StateStopped, 0, time.Time{} })
	s.logf("stopped")
	return nil
}

type result struct {
	exitCode   int
	clean      bool // ended normally (session over or shut down on request)
	registered bool
	reason     Reason
	detail     string
}

func (s *Supervisor) runOnce(ctx context.Context) result {
	dll, version, err := s.resolveRuntime()
	if err != nil {
		return result{exitCode: -1, reason: ReasonInvalidConfig, detail: err.Error()}
	}
	farm := game.Farm{GameDir: s.o.GameDir, Root: s.root(), RuntimeDLL: dll, Port: s.o.Port, PublisherLock: s.o.PublisherLock, ConfigYAML: s.o.ConfigYAML}
	if err := farm.Build(); err != nil {
		return result{exitCode: -1, reason: ReasonInvalidConfig, detail: "preparing instance: " + err.Error()}
	}
	if err := s.seedPrefix(); err != nil {
		return result{exitCode: -1, reason: ReasonOther, detail: "preparing Wine prefix: " + err.Error()}
	}

	args := []string{"./" + game.ExeName, "-server", "-headless", "-noovr", "-no-console", "-nosymbollookup", "-port", fmt.Sprint(s.o.Port)}
	if s.o.HTTPPort != 0 {
		args = append(args, "-httpport", fmt.Sprint(s.o.HTTPPort))
	}
	args = append(args, s.o.ExtraArgs...)
	cmd := exec.Command(s.o.Wine, args...)
	cmd.Dir = farm.ExeDir()
	cmd.Env = append(os.Environ(), "WINEPREFIX="+s.prefix(), "WINEDEBUG=-all", "WINEDLLOVERRIDES=mscoree,mshtml=")
	cmd.Env = append(cmd.Env, s.o.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A real pipe, so Wait does not block on wineserver, which inherits the
	// write end and outlives the game.
	pr, pw, err := os.Pipe()
	if err != nil {
		return result{exitCode: -1, reason: ReasonOther, detail: err.Error()}
	}
	defer pr.Close()
	cmd.Stdout, cmd.Stderr = pw, pw
	err = cmd.Start()
	pw.Close()
	if err != nil {
		return result{exitCode: -1, reason: ReasonOther, detail: "starting wine: " + err.Error()}
	}
	s.mu.Lock()
	s.proc = cmd.Process
	s.mu.Unlock()
	s.logf("starting %s on port %d (runtime %s)", s.o.Instance, s.o.Port, version)
	s.update(func(st *status.Status) {
		st.State, st.PID, st.RuntimeVersion, st.Reason, st.LastError, st.NextStart = status.StateStarting, cmd.Process.Pid, version, "", "", time.Time{}
	})

	exited := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		exited <- code
	}()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	var res result
	register := time.NewTimer(s.o.RegisterTimeout)
	defer register.Stop()
	killTimer := time.NewTimer(time.Hour)
	killTimer.Stop()
	defer killTimer.Stop()
	var killDeadline time.Time
	stopping := false
	killGroup := func(why string) {
		s.logf("%s; killing the game process", why)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// scheduleKill keeps the earliest deadline asked for.
	scheduleKill := func(d time.Duration) {
		if at := time.Now().Add(d); killDeadline.IsZero() || at.Before(killDeadline) {
			killDeadline = at
			killTimer.Reset(d)
		}
	}
	ctxDone := ctx.Done()
	for {
		select {
		case raw, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			ev := Parse(raw)
			if ev.Kind == KindNone {
				if s.o.Filter(ev.Line) {
					fmt.Fprintln(s.o.Console, raw)
				}
				continue
			}
			fmt.Fprintln(s.o.Console, raw)
			s.handle(ev, &res, register, scheduleKill, killGroup)
		case <-register.C:
			if !res.registered {
				res.reason, res.detail = ReasonRegistration, fmt.Sprintf("not registered within %s", s.o.RegisterTimeout)
				killGroup(res.detail)
			}
		case <-killTimer.C:
			if stopping {
				killGroup("the server did not exit after Ctrl+C")
			} else {
				killGroup("the game did not exit after shutting down (nevr-runtime #723)")
			}
		case <-ctxDone:
			ctxDone = nil
			stopping = true
			s.update(func(st *status.Status) { st.State = status.StateStopping })
			s.logf("stopping: sending Ctrl+C to the game")
			_ = cmd.Process.Signal(syscall.SIGINT)
			scheduleKill(s.o.StopTimeout)
		case code := <-exited:
			drain := time.After(time.Second) // what is left; wineserver may hold the pipe open
		loop:
			for lines != nil {
				select {
				case raw, ok := <-lines:
					if !ok {
						break loop
					}
					if ev := Parse(raw); ev.Kind != KindNone {
						fmt.Fprintln(s.o.Console, raw)
						s.handle(ev, &res, register, scheduleKill, func(string) {})
					}
				case <-drain:
					break loop
				}
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // leftover wine helpers
			s.wineserverKill()
			s.mu.Lock()
			s.proc = nil
			s.mu.Unlock()
			res.exitCode = code
			if stopping || (res.reason == ReasonNone && (res.clean || code == 0)) {
				res.clean = true
			}
			return res
		}
	}
}

func (s *Supervisor) handle(ev Event, res *result, register *time.Timer, scheduleKill func(time.Duration), killGroup func(string)) {
	switch ev.Kind {
	case KindRuntimeVersion:
		s.update(func(st *status.Status) { st.RuntimeVersion = ev.Version })
	case KindBound:
		if ev.Port != s.o.Port {
			res.reason, res.detail = ReasonInvalidConfig, fmt.Sprintf("game bound UDP port %d instead of %d", ev.Port, s.o.Port)
			killGroup(res.detail)
		}
	case KindRegistered:
		res.registered = true
		register.Stop()
		s.update(func(st *status.Status) {
			st.State, st.PublicIP, st.Registrations, st.Reason, st.LastError = status.StateRegistered, ev.IP, st.Registrations+1, "", ""
		})
		s.logf("%s registered as %s:%d", strings.TrimPrefix(ReadyMarker, "[evr] "), ev.IP, ev.Port)
	case KindSessionStart:
		s.update(func(st *status.Status) {
			st.State, st.Session, st.SessionsTotal = status.StateInSession, ev.Session, st.SessionsTotal+1
		})
	case KindSessionEnd:
		res.clean = true
		s.update(func(st *status.Status) { st.State = status.StateStopping })
	case KindTeardown:
		res.clean = res.reason == ReasonNone
		scheduleKill(s.o.TeardownGrace)
	case KindShutdown:
		if res.reason == ReasonNone {
			res.clean = true
		}
		scheduleKill(s.o.ShutdownGrace)
	case KindLoginFailed, KindFatal:
		if res.reason == ReasonNone || ev.Kind == KindFatal {
			res.reason, res.detail = ev.Reason, ev.Line
		}
		res.clean = false
		if ev.Kind == KindLoginFailed {
			// The server stays up but never registers; no point waiting.
			killGroup("login was rejected (check the Discord ID and password)")
		}
	}
	if s.o.OnEvent != nil {
		s.o.OnEvent(ev, s.Status())
	}
}

// resolveRuntime returns the DLL to use; "current" is re-read on every start so
// a runtime update takes effect at the next match boundary.
func (s *Supervisor) resolveRuntime() (dll, version string, err error) {
	dir := filepath.Join(s.o.NevrDir, s.o.Runtime)
	version = s.o.Runtime
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		version = filepath.Base(real)
		dir = real
	}
	dll = filepath.Join(dir, "BugSplat64.dll")
	if _, err := os.Stat(dll); err != nil {
		return "", version, fmt.Errorf("nevr-runtime %s is not installed: %w", s.o.Runtime, err)
	}
	return dll, version, nil
}

func (s *Supervisor) seedPrefix() error {
	marker := filepath.Join(s.prefix(), ".evr-seeded")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.prefix(), 0o755); err != nil {
		return err
	}
	if s.o.PrefixTemplate != "" {
		out, err := exec.Command("cp", "-a", s.o.PrefixTemplate+"/.", s.prefix()+"/").CombinedOutput()
		if err != nil {
			return fmt.Errorf("copy %s: %v: %s", s.o.PrefixTemplate, err, out)
		}
	}
	return os.WriteFile(marker, nil, 0o644)
}

func (s *Supervisor) wineserverKill() {
	cmd := exec.Command("wineserver", "-k")
	cmd.Env = append(os.Environ(), "WINEPREFIX="+s.prefix())
	_ = cmd.Run()
}

func (s *Supervisor) heartbeat(ctx context.Context) {
	t := time.NewTicker(s.o.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.update(func(*status.Status) {})
		}
	}
}

// Restart asks the running game to shut down gracefully; the loop starts a
// new one (immediately if it was waiting in backoff).
func (s *Supervisor) Restart() {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		_ = p.Signal(syscall.SIGINT)
		return
	}
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

func (s *Supervisor) readCommands(ctx context.Context) {
	sc := bufio.NewScanner(s.o.Commands)
	for sc.Scan() && ctx.Err() == nil {
		s.command(sc.Text())
	}
}

func (s *Supervisor) watchControl(ctx context.Context) {
	// A command left over from before this start is stale.
	_ = os.Remove(s.o.ControlFile)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b, err := os.ReadFile(s.o.ControlFile)
		if err != nil {
			continue
		}
		_ = os.Remove(s.o.ControlFile)
		for _, line := range strings.Split(string(b), "\n") {
			s.command(line)
		}
	}
}

func (s *Supervisor) command(text string) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "":
	case "status":
		st := s.Status()
		s.logf("state=%s port=%d ip=%s runtime=%s sessions=%d restarts=%d %s", st.State, st.Port, st.PublicIP, st.RuntimeVersion, st.SessionsTotal, st.Restarts, st.LastError)
	case "restart":
		s.logf("restart requested")
		s.Restart()
	case "help":
		s.logf("commands: status, restart, help (stop with the panel's stop button or Ctrl+C)")
	default:
		s.logf("unknown command %q (try: status, restart, help)", strings.TrimSpace(text))
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

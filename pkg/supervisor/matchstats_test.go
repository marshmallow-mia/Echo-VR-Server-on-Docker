package supervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runLoop runs the match loop until the test ends (before its temp dir goes).
func runLoop(t *testing.T, s *Supervisor) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.matchLoop(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestMatchLoopFollowsAMatch(t *testing.T) {
	var blue atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"sessionid":"ABC","game_status":"playing","game_clock_display":"03:00.00","match_type":"Echo_Arena","map_name":"mpl_arena_a",
			"blue_points":` + strconv.Itoa(int(blue.Load())) + `,"orange_points":1,"last_score":{"team":"blue","point_amount":2,"person_scored":"Ava","disc_speed":12.5},
			"teams":[{"team":"BLUE TEAM","players":[{"name":"Ava","userid":1001,"ping":40}]},{"team":"ORANGE TEAM","players":[{"name":"N","userid":5}]},{"team":"SPECTATORS"}]}`))
	}))
	defer srv.Close()
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.MatchPoll = 30 * time.Millisecond
	s := newSup(t, o)
	runLoop(t, s)

	s.update(func(st *status.Status) {
		st.State, st.Session, st.HTTPPort, st.SessionAPI, st.GameType, st.Map = status.StateInSession, "abc", port, "on", "echo_arena", "mpl_arena_a"
	})
	waitFor(t, "first answer", func() bool { m, err := match.ReadMatch(o.Home); return err == nil && m.API == match.APIOn })
	blue.Store(2)
	waitFor(t, "goal", func() bool {
		m, _ := match.ReadMatch(o.Home)
		return m.Blue != nil && m.Blue.Points == 2 && len(m.Events) > 0 && m.Events[0].Type == match.EventGoal
	})
	m, _ := match.ReadMatch(o.Home)
	if !m.Active || m.SessionID != "abc" || m.GameType != "echo_arena" || m.Players != 2 || m.Blue.Players[0].Name != "Ava" {
		t.Fatalf("match.json: %+v", m)
	}

	// The match ends and the game exits.
	s.update(func(st *status.Status) { st.State, st.Session = status.StateRegistered, "" })
	waitFor(t, "history", func() bool { h, err := match.ReadHistory(o.Home); return err == nil && len(h.Matches) == 1 })
	h, _ := match.ReadHistory(o.Home)
	if h.Matches[0].Winner != "blue" || h.Matches[0].Blue != 2 || len(h.Matches[0].Players) != 2 {
		t.Errorf("summary: %+v", h.Matches[0])
	}
	if m, _ := match.ReadMatch(o.Home); m.Active || m.EndedAt.IsZero() {
		t.Errorf("match.json still active: %+v", m)
	}
}

func TestMatchLoopAPIOff(t *testing.T) {
	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.MatchPoll = 30 * time.Millisecond
	s := newSup(t, o)
	runLoop(t, s)
	s.update(func(st *status.Status) {
		st.State, st.Session, st.SessionAPI, st.GameType, st.Map = status.StateInSession, "abc", "off", "echo_combat", "mpl_combat_dyson"
	})
	waitFor(t, "log-only match", func() bool {
		m, err := match.ReadMatch(o.Home)
		return err == nil && m.API == match.APIOff && m.Map == "mpl_combat_dyson" && m.Source == "log"
	})
}

func TestStartAppliesAPISetting(t *testing.T) {
	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	s := newSup(t, o)
	settings := game.SettingsPath(s.prefix(), s.o.User)
	s.runOnce(context.Background())
	if on, err := game.APIAccess(settings); err != nil || !on {
		t.Fatalf("default: API %v %v", on, err)
	}
	if st := s.Status(); st.SessionAPI != "on" {
		t.Errorf("status session_api %q", st.SessionAPI)
	}
	if err := match.SetAPI(o.Home, false); err != nil {
		t.Fatal(err)
	}
	s.runOnce(context.Background())
	if on, _ := game.APIAccess(settings); on {
		t.Error("off not applied at start")
	}
}

func TestAPICommand(t *testing.T) {
	o, out := testOptions(t, "registered-sigint.jsonl", 6795)
	s := newSup(t, o)
	// In a match: saved, applied later, no restart.
	s.update(func(st *status.Status) { st.State, st.PID, st.SessionAPI = status.StateInSession, 99999999, "on" })
	s.command("API  off")
	if match.LoadSettings(o.Home).APIOn() {
		t.Fatal("setting not saved")
	}
	if st := s.Status(); st.SessionAPINext != "off" {
		t.Errorf("next = %q", st.SessionAPINext)
	}
	if !strings.Contains(out.String(), "after this match") {
		t.Errorf("console: %s", out)
	}
	// Back to what the game runs: nothing pending.
	s.command("api on")
	if st := s.Status(); st.SessionAPINext != "" {
		t.Errorf("next after undo = %q", st.SessionAPINext)
	}
	s.command("api status")
	if !strings.Contains(out.String(), "match stats API: on (setting: on)") {
		t.Errorf("status line: %s", out)
	}
}

func TestMatchLoopWritesLivePositions(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Write([]byte(`{"sessionid":"ABC","game_status":"playing","match_type":"Echo_Arena","map_name":"mpl_arena_a",
			"disc":{"position":[0.04,1,` + strconv.Itoa(int(n)) + `]},
			"teams":[{"team":"BLUE TEAM","players":[{"name":"Ava","userid":1001,"possession":true,"head":{"position":[1.04,2,-30]}}]},
			{"team":"ORANGE TEAM","players":[{"name":"N","userid":5,"stunned":true,"head":{"position":[-2,1.5,25.06]}},{"name":"NoHead","userid":6}]},
			{"team":"SPECTATORS","players":[{"name":"Spec","head":{"position":[0,0,0]}}]}]}`))
	}))
	defer srv.Close()
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)

	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.MatchPoll = time.Hour // only the first round is a full one
	o.LivePoll = 20 * time.Millisecond
	s := newSup(t, o)
	runLoop(t, s)
	s.update(func(st *status.Status) {
		st.State, st.Session, st.HTTPPort, st.SessionAPI, st.GameType, st.Map = status.StateInSession, "abc", port, "on", "echo_arena", "mpl_arena_a"
	})
	waitFor(t, "live.json moving", func() bool {
		l, err := match.ReadLive(o.Home)
		return err == nil && len(l.Disc) == 3 && l.Disc[2] >= 5
	})
	m, err := match.ReadMatch(o.Home)
	if err != nil || m.Disc == nil || m.Disc.Pos[2] != 1 {
		t.Errorf("match.json was rewritten between full rounds: %+v %v", m.Disc, err)
	}
	l, _ := match.ReadLive(o.Home)
	if l.SessionID != "abc" || l.Map != "mpl_arena_a" || l.AtMS == 0 || len(l.Players) != 2 {
		t.Fatalf("live.json: %+v", l)
	}
	if p := l.Players[0]; p.Name != "Ava" || p.Side != "blue" || !p.Possession || p.Pos[0] != 1 || p.Pos[2] != -30 {
		t.Errorf("blue: %+v", p)
	}
	if p := l.Players[1]; p.Name != "N" || p.Side != "orange" || !p.Stunned || p.Pos[2] != 25.1 {
		t.Errorf("orange: %+v", p)
	}

	// The match ends: live.json goes.
	s.update(func(st *status.Status) { st.State, st.Session = status.StateRegistered, "" })
	waitFor(t, "live.json removed", func() bool { _, err := match.ReadLive(o.Home); return errors.Is(err, os.ErrNotExist) })
}

func TestMatchLoopLiveOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sessionid":"ABC","match_type":"Echo_Arena","teams":[{"team":"BLUE TEAM","players":[{"name":"V","head":{"position":[1,2,3]}}]}]}`))
	}))
	defer srv.Close()
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.MatchPoll = 30 * time.Millisecond
	o.LivePoll = -1
	s := newSup(t, o)
	runLoop(t, s)
	s.update(func(st *status.Status) {
		st.State, st.Session, st.HTTPPort, st.SessionAPI, st.GameType = status.StateInSession, "abc", port, "on", "echo_arena"
	})
	waitFor(t, "first answer", func() bool { m, err := match.ReadMatch(o.Home); return err == nil && m.API == match.APIOn })
	time.Sleep(100 * time.Millisecond)
	if _, err := match.ReadLive(o.Home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("live.json written with LivePoll off: %v", err)
	}
}

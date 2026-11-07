package supervisor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	spv1 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/spatial/v1"
	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/telemetry"
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

func TestLobbyLiveFromTheStream(t *testing.T) {
	now := time.Now()
	st := status.Status{Session: "4C23-A", Map: "mpl_lobby_b2", GameType: "social_2.0"}
	f := telemetry.Frame{Session: "4c23-a", Map: "header-map", At: now.Add(-300 * time.Millisecond), Seats: []telemetry.Seat{
		{Name: "Ava", Side: "social", Pos: [3]float64{-12, 1.6, 3.1}},
		{Name: "Spec", Side: "spectator", Pos: [3]float64{1, 1, 1}},
		{Name: "Bo", Side: "blue", Pos: [3]float64{-60, 1, -30}},
	}}
	l, ok := lobbyLive(f, st, now)
	if !ok || l.SessionID != "4c23-a" || l.Map != "mpl_lobby_b2" || l.AtMS != f.At.UnixMilli() || len(l.Players) != 2 {
		t.Fatalf("live: %+v %v", l, ok)
	}
	if p := l.Players[0]; p.Name != "Ava" || p.Side != "social" || p.Pos[0] != -12 || p.Pos[2] != 3.1 {
		t.Errorf("social player: %+v", p)
	}
	if p := l.Players[1]; p.Name != "Bo" || p.Side != "blue" {
		t.Errorf("blue player: %+v", p)
	}
	st.Map = ""
	if l, _ := lobbyLive(f, st, now); l.Map != "header-map" {
		t.Errorf("no map from the log: %q", l.Map)
	}
	for name, g := range map[string]telemetry.Frame{
		"no frame":        {},
		"stale":           {Session: "4C23-A", At: now.Add(-3 * time.Second), Seats: f.Seats},
		"other session":   {Session: "OLD", At: now, Seats: f.Seats},
		"no positions":    {Session: "4C23-A", At: now},
		"only spectators": {Session: "4C23-A", At: now, Seats: f.Seats[1:2]},
	} {
		if l, ok := lobbyLive(g, st, now); ok {
			t.Errorf("%s: %+v", name, l)
		}
	}
	if _, ok := lobbyLive(telemetry.Frame{At: now, Seats: f.Seats}, st, now); !ok {
		t.Error("a header without a session id is taken as this session's")
	}
}

func TestMatchLoopLobbyLiveFollowsTheStream(t *testing.T) {
	o, _ := testOptions(t, "registered-sigint.jsonl", 6795)
	o.MatchPoll = time.Hour
	o.LivePoll = 20 * time.Millisecond
	s := newSup(t, o)
	runLoop(t, s)
	s.update(func(st *status.Status) {
		// No HTTP port: the game's /session is never asked in a lobby.
		st.State, st.Session, st.SessionAPI, st.GameType, st.Map = status.StateInSession, "4C23-A", "on", "social_2.0", "mpl_lobby_b2"
	})
	d := telemetry.Decoder{Latest: s.latest}
	send := func(m proto.Message) {
		b, _ := proto.Marshal(m)
		if err := d.Envelope(b); err != nil {
			t.Fatal(err)
		}
	}
	send(&tv2.Envelope{Message: &tv2.Envelope_Header{Header: &tv2.CaptureHeader{GameType: "social_2.0",
		GameHeader: &tv2.CaptureHeader_EchoArena{EchoArena: &tv2.EchoArenaHeader{SessionId: "4C23-A", MapName: "mpl_lobby_b2"}}}}})
	frame := func(z float32) *tv2.Envelope {
		return &tv2.Envelope{Message: &tv2.Envelope_Frame{Frame: &tv2.Frame{Payload: &tv2.Frame_EchoArena{EchoArena: &tv2.EchoArenaFrame{
			Events: []*tv2.EchoEvent{{Event: &tv2.EchoEvent_PlayerJoined{PlayerJoined: &tv2.PlayerJoined{Slot: 2, DisplayName: "Ava",
				Role: tv2.Role_ROLE_SOCIAL_PARTICIPANT}}}},
			Players: []*tv2.PlayerState{{Slot: 2, Head: &spv1.Pose{Position: &spv1.Vec3{X: -12, Y: 1.6, Z: z}}}}}}}}}
	}
	send(frame(3))
	waitFor(t, "lobby live.json", func() bool { l, err := match.ReadLive(o.Home); return err == nil && len(l.Players) == 1 })
	send(frame(8))
	waitFor(t, "the player moves", func() bool { l, _ := match.ReadLive(o.Home); return len(l.Players) == 1 && l.Players[0].Pos[2] == 8 })
	l, _ := match.ReadLive(o.Home)
	if p := l.Players[0]; l.SessionID != "4c23-a" || l.Map != "mpl_lobby_b2" || p.Name != "Ava" || p.Side != "social" || p.Pos[0] != -12 {
		t.Errorf("live.json: %+v", l)
	}
	if _, err := os.Stat(filepath.Join(o.Home, match.FileName)); err != nil {
		t.Errorf("match.json: %v", err)
	}

	// The stream stops sending: the map goes away after lobbyStale.
	waitFor(t, "stale live.json removed", func() bool { _, err := match.ReadLive(o.Home); return errors.Is(err, os.ErrNotExist) })
}

package match

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sess(bluePts, orangePts float64, status string, blue, orange []APIPlayer) *Session {
	return &Session{SessionID: "DCCB76E5-CBAE-45F4-A1CB-66B121A22104", GameStatus: status, GameClockDisplay: "04:12.30", GameClock: 252.3,
		MatchType: "Echo_Arena", MapName: "mpl_arena_a", BluePoints: bluePts, OrangePoints: orangePts, TotalRoundCount: 1,
		Pause: &Pause{PausedState: "unpaused"},
		Teams: []APITeam{
			{Team: "BLUE TEAM", Players: blue, Stats: &Stats{Points: int(bluePts)}},
			{Team: "ORANGE TEAM", Players: orange, Stats: &Stats{Points: int(orangePts)}},
			{Team: "SPECTATORS"},
		}}
}

var (
	ava = APIPlayer{Name: "Ava", UserID: 1001, PlayerID: 2, Number: 7, Level: 50, Ping: 41, Stats: &Stats{}}
	big = APIPlayer{Name: "N", UserID: 1402761802751676571, PlayerID: 5, Number: 1, Level: 12, Ping: 80, Stats: &Stats{}}
)

func TestTrackerGoalsJoinsAndSummary(t *testing.T) {
	var tr Tracker
	start := time.Date(2026, 11, 4, 17, 30, 3, 0, time.UTC)
	tr.Start("dccb76e5-cbae-45f4-a1cb-66b121a22104", "echo_arena", "mpl_arena_a", start)
	tr.Observe(sess(0, 0, "pre_match", []APIPlayer{ava}, nil), start.Add(time.Second))
	if m := tr.Match(); len(m.Events) != 0 || m.API != APIOn || m.Players != 1 {
		t.Fatalf("baseline: %+v", m)
	}

	// N joins orange, the round starts, Ava scores 2 for blue.
	s := sess(2, 0, "playing", []APIPlayer{ava}, []APIPlayer{big})
	s.LastScore = &LastScore{Team: "blue", PointAmount: 2, PersonScored: "Ava", DiscSpeed: 14.237, DistanceThrown: 7.5, GoalType: "SLAM DUNK"}
	tr.Observe(s, start.Add(3*time.Second))
	m := tr.Match()
	if m.Blue.Points != 2 || m.Orange.Points != 0 || m.Players != 2 {
		t.Fatalf("score/players: %+v %+v", m.Blue, m.Orange)
	}
	if m.Orange.Players[0].UserID != "1402761802751676571" {
		t.Errorf("big user id kept as text: %q", m.Orange.Players[0].UserID)
	}
	if m.LastGoal == nil || m.LastGoal.Scorer != "Ava" || m.LastGoal.Speed != 14.24 {
		t.Errorf("last goal: %+v", m.LastGoal)
	}
	types := map[string]Event{}
	for _, e := range m.Events {
		types[e.Type] = e
	}
	if g := types[EventGoal]; g.Team != "blue" || !strings.Contains(g.Text, "Ava") || !strings.Contains(g.Text, "14.2 m/s") {
		t.Errorf("goal event: %+v", g)
	}
	if j := types[EventJoin]; j.Player != "N" || j.Team != "orange" {
		t.Errorf("join event: %+v", j)
	}

	// N leaves, the match ends.
	tr.Observe(sess(2, 0, "post_match", []APIPlayer{ava}, nil), start.Add(5*time.Second))
	m = tr.Match()
	if m.Events[0].Type != EventLeave && m.Events[1].Type != EventLeave {
		t.Errorf("leave event missing: %+v", m.Events)
	}
	sum, ok := tr.Finish(start.Add(6 * time.Second))
	if !ok || sum.Winner != "blue" || sum.Duration != 6 || sum.Blue != 2 || len(sum.Players) != 2 {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.Players[1].Name != "N" || sum.Players[1].Team != "orange" {
		t.Errorf("a player who left stays in the summary: %+v", sum.Players)
	}
	if tr.Active() {
		t.Error("still active after Finish")
	}
}

func TestTrackerLogOnly(t *testing.T) {
	var tr Tracker
	tr.Start("abc", "echo_combat", "", time.Now())
	tr.SetLog("", "mpl_combat_dyson")
	tr.SetAPI(APIOff, "")
	m := tr.Match()
	if m.Source != "log" || m.API != APIOff || m.Map != "mpl_combat_dyson" || m.GameType != "echo_combat" || !m.Active {
		t.Fatalf("log-only match: %+v", m)
	}
	sum, _ := tr.Finish(time.Now())
	if sum.Winner != "" || sum.Source != "log" {
		t.Errorf("log-only summary: %+v", sum)
	}
}

func TestParseSession(t *testing.T) {
	if _, err := ParseSession(500, []byte(`{"err_description":"API setting is not enabled check settings menu","err_code":-2}`)); err != ErrDisabled {
		t.Errorf("disabled: %v", err)
	}
	if _, err := ParseSession(http.StatusNotFound, []byte(`<html>404</html>`)); err != ErrNoSession {
		t.Errorf("404: %v", err)
	}
	s, err := ParseSession(200, []byte(`{"sessionid":"X","game_status":"playing","blue_points":3,"teams":[{"team":"BLUE TEAM","players":[{"name":"a","userid":18446744073709551615}]}]}`))
	if err != nil || s.BluePoints != 3 || s.Teams[0].Players[0].UserID != 18446744073709551615 {
		t.Errorf("ok answer: %+v %v", s, err)
	}
}

func TestHistoryKeepsNewestFirst(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < HistoryMax+5; i++ {
		if err := AppendHistory(home, Summary{SessionID: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := ReadHistory(home)
	if err != nil || len(h.Matches) != HistoryMax || h.Matches[0].SessionID != strconv.Itoa(HistoryMax+4) {
		t.Fatalf("history: %d %v first=%q", len(h.Matches), err, h.Matches[0].SessionID)
	}
}

func TestSettingsDefaultOn(t *testing.T) {
	home := t.TempDir()
	if !LoadSettings(home).APIOn() {
		t.Fatal("default must be on")
	}
	if err := SetAPI(home, false); err != nil {
		t.Fatal(err)
	}
	if LoadSettings(home).APIOn() {
		t.Fatal("off not kept")
	}
	b, _ := os.ReadFile(filepath.Join(home, SettingsFile))
	if !strings.Contains(string(b), `"session_api": false`) {
		t.Errorf("file: %s", b)
	}
	if !LoadSettings(home).TelemetryOn() {
		t.Fatal("telemetry default must be on")
	}
	if err := SetTelemetry(home, false); err != nil || LoadSettings(home).TelemetryOn() {
		t.Fatalf("telemetry off not kept: %v", err)
	}
}

// TestCombatFixture replays /session answers captured on echo during a pub
// combat match on Dyson (2026-11-04, names and ids replaced).
func TestCombatFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/combat-dyson.json")
	if err != nil {
		t.Fatal(err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(b, &raws); err != nil {
		t.Fatal(err)
	}
	var tr Tracker
	now := time.Date(2026, 11, 4, 17, 53, 43, 0, time.UTC)
	tr.Start("0cad10e9-0000-0000-0000-000000000000", "echo_combat", "mpl_combat_dyson", now)
	for i, raw := range raws {
		s, err := ParseSession(200, raw)
		if err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
		tr.Observe(s, now.Add(time.Duration(i)*6*time.Second))
	}
	m := tr.Match()
	if !m.Percent || m.MatchType != "Echo_Combat" || m.Blue.Points <= 0 {
		t.Fatalf("combat match: percent=%v type=%q blue=%v", m.Percent, m.MatchType, m.Blue.Points)
	}
	for _, e := range m.Events {
		if e.Type == EventGoal {
			t.Errorf("capture progress made a goal event: %+v", e)
		}
	}
	if m.Players == 0 || m.Blue.Players[0].Weapon == "" {
		t.Errorf("players/loadouts missing: %+v", m.Blue.Players)
	}
	if m.Paused != "" {
		t.Errorf("combat's paused_state none read as paused: %q", m.Paused)
	}
	if got := Line(m); !strings.Contains(got, "Combat on Dyson") || !strings.Contains(got, "%") {
		t.Errorf("line: %q", got)
	}
}

func TestLiveExtras(t *testing.T) {
	raw := []byte(`{"sessionid":"S","game_status":"playing","match_type":"Echo_Arena","blue_points":0,"orange_points":0,
		"blue_team_restart_request":1,"orange_team_restart_request":false,"rules_changed_by":"Ava",
		"pause":{"paused_state":"paused","paused_requested_team":"orange","paused_timer":12.5},
		"disc":{"position":[0.04,1.21,-3.96],"bounce_count":2},
		"teams":[{"team":"BLUE TEAM","players":[{"name":"Ava","userid":1001,"ping":41,"stunned":true,"blocking":true,
			"is_emote_playing":true,"Arm":"Left","head":{"position":[2.33,1.5,-10.04]}}]},
			{"team":"ORANGE TEAM","players":[{"name":"N","userid":5,"ping":80,"head":{"position":[-4.1,1.6,20]}}]}]}`)
	var tr Tracker
	tr.Start("s", "echo_arena", "mpl_arena_a", time.Now())
	for i, ping := range []int{41, 45, 60} {
		s, err := ParseSession(200, []byte(strings.Replace(string(raw), `"ping":41`, `"ping":`+strconv.Itoa(ping), 1)))
		if err != nil {
			t.Fatal(err)
		}
		tr.Observe(s, time.Now().Add(time.Duration(i)*time.Second))
	}
	m := tr.Match()
	p := m.Blue.Players[0]
	if !p.Stunned || !p.Blocking || !p.Emote || p.Arm != "Left" || len(p.Pos) != 3 || p.Pos[0] != 2.3 {
		t.Errorf("player: %+v", p)
	}
	if got := p.PingTrend; len(got) != 3 || got[0] != 41 || got[2] != 60 {
		t.Errorf("ping trend: %v", got)
	}
	if m.Disc == nil || m.Disc.Bounces != 2 || m.Disc.Pos[2] != -4 {
		t.Errorf("disc: %+v", m.Disc)
	}
	if b := m.Bounds; b == nil || b.MinX != -4.1 || b.MaxX != 2.3 || b.MinZ != -10 || b.MaxZ != 20 {
		t.Errorf("bounds: %+v", b)
	}
	if m.RestartRequest != "blue" || m.RulesChangedBy != "Ava" || m.Paused != "paused" || m.PausedBy != "orange" || m.PauseTimer != 12.5 {
		t.Errorf("restart/rules/pause: %q %q %q %q %v", m.RestartRequest, m.RulesChangedBy, m.Paused, m.PausedBy, m.PauseTimer)
	}
}

func TestRulesChangedByInvalid(t *testing.T) {
	var tr Tracker
	tr.Start("s", "echo_arena", "mpl_arena_a", time.Now())
	s := sess(0, 0, "playing", []APIPlayer{ava}, nil)
	s.RulesChangedBy = "[INVALID]"
	tr.Observe(s, time.Now())
	if got := tr.Match().RulesChangedBy; got != "" {
		t.Errorf("rules changed by %q", got)
	}
}

func TestLastGoalDropsGarbage(t *testing.T) {
	var tr Tracker
	tr.Start("s", "echo_arena", "mpl_arena_a", time.Now())
	s := sess(2, 0, "score", []APIPlayer{ava}, nil)
	s.LastScore = &LastScore{Team: "blue", PointAmount: 2, PersonScored: "Ava", AssistScored: "Ava", DiscSpeed: -9.679977999999999e23, GoalType: "SLAM DUNK"}
	tr.Observe(s, time.Now())
	if g := tr.Match().LastGoal; g == nil || g.Speed != 0 || g.Assist != "" || g.Distance != 0 {
		t.Errorf("last goal: %+v", g)
	}
}

func TestPlaysFromSession(t *testing.T) {
	var tr Tracker
	now := time.Now()
	tr.Start("s", "echo_arena", "mpl_arena_a", now)
	v, n := ava, big
	v.Stats, n.Stats = &Stats{Stuns: 1}, &Stats{}
	tr.Observe(sess(0, 0, "playing", []APIPlayer{v}, []APIPlayer{n}), now)
	v.Stats, n.Stats = &Stats{Stuns: 3, Passes: 2}, &Stats{Saves: 1, Steals: 1}
	tr.Observe(sess(0, 0, "playing", []APIPlayer{v}, []APIPlayer{n}), now.Add(2*time.Second))
	got := map[string]bool{}
	for _, e := range tr.Match().Plays {
		got[e.Text] = true
		if e.Clock != "04:12.30" || e.Type != EventPlay {
			t.Errorf("play: %+v", e)
		}
	}
	if len(got) != 3 || !got["Ava stuns (3)"] || !got["N saves (1)"] || !got["N steals the disc (1)"] {
		t.Errorf("plays: %v", got)
	}
}

func TestPlaysFromTelemetry(t *testing.T) {
	var tr Tracker
	now := time.Now()
	tr.Start("s", "echo_combat", "mpl_combat_dyson", now)
	tr.SetLog("", "mpl_combat_dyson")
	tr.SetAPI(APIOff, "")
	tr.Note(Note{Kind: NoteTeam, At: now, Player: "Ava", Team: "blue"})
	tr.Note(Note{Kind: NotePlay, At: now, Player: "Ava", Team: "blue", Play: "stun", Total: 1, Text: "Ava stuns (1)"})
	tr.Note(Note{Kind: NotePlay, At: now, Player: "Ava", Team: "blue", Play: "stun", Total: 2, Text: "Ava stuns (2)"})
	tr.Note(Note{Kind: NotePlay, At: now, Player: "Ava", Team: "blue", Play: "pass", Total: 1})
	m := tr.Match()
	if len(m.Plays) != 2 || m.Plays[0].Text != "Ava stuns (2)" {
		t.Fatalf("plays: %+v", m.Plays)
	}
	if len(m.Blue.Players) != 1 || m.Blue.Players[0].Stats == nil || m.Blue.Players[0].Stats.Stuns != 2 || m.Blue.Players[0].Stats.Passes != 1 {
		t.Fatalf("totals: %+v", m.Blue.Players)
	}
	// The tracker's own roster keeps no stats pointer the views could change.
	m.Blue.Players[0].Stats.Stuns = 99
	if tr.Match().Blue.Players[0].Stats.Stuns != 2 {
		t.Error("Match() shares the stats")
	}
}

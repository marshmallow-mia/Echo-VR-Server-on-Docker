package match

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version is match.json's and match-history.json's format version.
const Version = 1

// HistoryMax is how many finished matches match-history.json keeps.
const HistoryMax = 50

// EventsMax is how many events match.json keeps.
const EventsMax = 30

// API states in Match.API.
const (
	APIOn          = "on"          // the game answers /session
	APIOff         = "off"         // switched off in the instance's settings
	APIDisabled    = "disabled"    // switched on, but the running game still has it off (restart pending)
	APIWaiting     = "waiting"     // on, but no answer yet (loading, no port seen)
	APIUnavailable = "unavailable" // on, but the game does not answer
)

// Match is match.json: the match a server hosts now (Active) or hosted last.
// Everything from the session API is empty when API is not "on"; game type,
// level and session id come from the game's log and are always there.
type Match struct {
	Version      int       `json:"version"`
	UpdatedAt    time.Time `json:"updated_at"`
	Active       bool      `json:"active"`
	API          string    `json:"api"`
	APIError     string    `json:"api_error,omitempty"`
	Source       string    `json:"source"` // "session_api" or "log"
	SessionID    string    `json:"session_id,omitempty"`
	StartedAt    time.Time `json:"started_at,omitzero"`
	EndedAt      time.Time `json:"ended_at,omitzero"`
	GameType     string    `json:"game_type,omitempty"` // echo_arena, echo_combat, … (log)
	Map          string    `json:"map,omitempty"`       // mpl_arena_a, mpl_combat_dyson, … (log, else API)
	MatchType    string    `json:"match_type,omitempty"`
	Private      bool      `json:"private,omitempty"`
	Tournament   bool      `json:"tournament,omitempty"`
	Status       string    `json:"status,omitempty"` // game_status: pre_match, round_start, playing, score, round_over, post_match, …
	Clock        float64   `json:"clock,omitempty"`
	ClockDisplay string    `json:"clock_display,omitempty"`
	Paused       string    `json:"paused,omitempty"` // paused_state when paused
	PausedBy     string    `json:"paused_by,omitempty"`
	PauseTimer   float64   `json:"pause_timer,omitempty"` // seconds the pause has left or lasted
	// RestartRequest: a team asked for a restart (blue, orange, both).
	RestartRequest string `json:"restart_request,omitempty"`
	RulesChangedBy string `json:"rules_changed_by,omitempty"`
	// Phase is where the match is, from the game's log (Phase* constants);
	// Round counts the rounds started, RoundStartedAt is when the current
	// one's play began (combat has no clock, so views count from there).
	Phase          string    `json:"phase,omitempty"`
	PhaseSince     time.Time `json:"phase_since,omitzero"`
	Round          int       `json:"round,omitempty"`
	RoundStartedAt time.Time `json:"round_started_at,omitzero"`
	Contested      bool      `json:"contested,omitempty"`
	// Percent: the points are combat's capture progress (0–100), not goals.
	Percent    bool     `json:"percent,omitempty"`
	Rounds     *Rounds  `json:"rounds,omitempty"`
	Blue       *Team    `json:"blue,omitempty"`
	Orange     *Team    `json:"orange,omitempty"`
	Spectators []Player `json:"spectators,omitempty"`
	Players    int      `json:"players"` // on the two teams
	LastGoal   *Goal    `json:"last_goal,omitempty"`
	Disc       *Disc    `json:"disc,omitempty"`
	// Bounds are the smallest and largest x and z (floor plane) any player or
	// the disc had in this match, for drawing a map of Pos.
	Bounds  *Bounds  `json:"bounds,omitempty"`
	Payload *Payload `json:"payload,omitempty"`
	Events  []Event  `json:"events,omitempty"` // newest first
	// Plays are players' actions from nevr-runtime's telemetry stream (stuns,
	// saves, steals, …), newest first; Telemetry says whether it is connected.
	Plays     []Event `json:"plays,omitempty"`
	Telemetry bool    `json:"telemetry,omitempty"`
}

// Rounds are the round scores.
type Rounds struct {
	Total  int `json:"total"`
	Blue   int `json:"blue"`
	Orange int `json:"orange"`
}

// Team is one side.
type Team struct {
	Points     float64  `json:"points"`
	Possession bool     `json:"possession,omitempty"`
	Stats      *Stats   `json:"stats,omitempty"`
	Players    []Player `json:"players"`
}

// Player is one player. UserID is a string: platform ids exceed what
// JavaScript and PHP floats hold exactly.
type Player struct {
	Name       string  `json:"name"`
	UserID     string  `json:"user_id,omitempty"`
	Slot       int     `json:"slot"`
	Number     int     `json:"number"`
	Level      int     `json:"level"`
	Ping       int     `json:"ping"`
	PacketLoss float64 `json:"packet_loss"`
	Stunned    bool    `json:"stunned,omitempty"`
	Possession bool    `json:"possession,omitempty"`
	Blocking   bool    `json:"blocking,omitempty"`
	Emote      bool    `json:"emote,omitempty"`
	Arm        string  `json:"arm,omitempty"`
	// Pos is the head's position [x, y, z] in metres (y is up).
	Pos []float64 `json:"pos,omitempty"`
	// PingTrend is the ping of the last PingTrendMax answers, oldest first.
	PingTrend []int  `json:"ping_trend,omitempty"`
	Weapon    string `json:"weapon,omitempty"`
	Ordnance  string `json:"ordnance,omitempty"`
	TacMod    string `json:"tac_mod,omitempty"`
	Stats     *Stats `json:"stats,omitempty"`
}

// Goal is the latest score.
type Goal struct {
	Team     string  `json:"team"`
	Points   int     `json:"points"`
	Type     string  `json:"type,omitempty"`
	Scorer   string  `json:"scorer,omitempty"`
	Assist   string  `json:"assist,omitempty"`
	Speed    float64 `json:"speed"`
	Distance float64 `json:"distance"`
}

// Disc is the disc in arena.
type Disc struct {
	Pos     []float64 `json:"pos,omitempty"`
	Bounces int       `json:"bounces"`
}

// Bounds is a box on the floor plane.
type Bounds struct {
	MinX float64 `json:"min_x"`
	MaxX float64 `json:"max_x"`
	MinZ float64 `json:"min_z"`
	MaxZ float64 `json:"max_z"`
}

func (b *Bounds) add(pos []float64) *Bounds {
	if len(pos) < 3 || (pos[0] == 0 && pos[2] == 0) {
		return b
	}
	x, z := pos[0], pos[2]
	if b == nil {
		return &Bounds{MinX: x, MaxX: x, MinZ: z, MaxZ: z}
	}
	b.MinX, b.MaxX, b.MinZ, b.MaxZ = math.Min(b.MinX, x), math.Max(b.MaxX, x), math.Min(b.MinZ, z), math.Max(b.MaxZ, z)
	return b
}

// PingTrendMax is how many pings Player.PingTrend keeps.
const PingTrendMax = 30

// Payload is combat's payload state.
type Payload struct {
	Checkpoint int     `json:"checkpoint"`
	Distance   float64 `json:"distance"`
	Defenders  int     `json:"defenders"`
	Speed      float64 `json:"speed"`
	Multiplier float64 `json:"multiplier"`
}

// Event types.
const (
	EventGoal   = "goal"
	EventRound  = "round"
	EventStatus = "status"
	EventPause  = "pause"
	EventJoin   = "join"
	EventLeave  = "leave"
	EventSwitch = "switch"
	EventPlay   = "play"
)

// Event is something that happened in the match.
type Event struct {
	At     time.Time `json:"at"`
	Clock  string    `json:"clock,omitempty"`
	Type   string    `json:"type"`
	Team   string    `json:"team,omitempty"`
	Player string    `json:"player,omitempty"`
	Text   string    `json:"text"`
}

// Summary is one finished match in match-history.json.
type Summary struct {
	SessionID string          `json:"session_id"`
	GameType  string          `json:"game_type,omitempty"`
	Map       string          `json:"map,omitempty"`
	MatchType string          `json:"match_type,omitempty"`
	Private   bool            `json:"private,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at"`
	Duration  int             `json:"duration_s"`
	Source    string          `json:"source"`
	Blue      float64         `json:"blue_points"`
	Orange    float64         `json:"orange_points"`
	Percent   bool            `json:"percent,omitempty"`
	Rounds    *Rounds         `json:"rounds,omitempty"`
	Winner    string          `json:"winner,omitempty"` // blue, orange, tie
	Players   []SummaryPlayer `json:"players,omitempty"`
}

// SummaryPlayer is a player as last seen in a match.
type SummaryPlayer struct {
	Name   string `json:"name"`
	UserID string `json:"user_id,omitempty"`
	Team   string `json:"team"` // blue, orange, spectator
	Stats  *Stats `json:"stats,omitempty"`
}

// History is match-history.json.
type History struct {
	Version int       `json:"version"`
	Matches []Summary `json:"matches"` // newest first
}

// Tracker turns /session answers into a Match, with events found by
// comparing each answer with the one before.
type Tracker struct {
	m      Match
	prev   *Session
	roster map[string]SummaryPlayer // by user id (name when there is none)
	order  []string

	// From the game's log.
	logRoster    map[string]*logPlayer // by name
	logOrder     []string
	logPhases    bool // phases come from the log: no status events from /session
	logPlayers   bool // joins and leaves come from the log: none from /session
	roundPlaying bool
	pending      *pendingLeave

	pings     map[string][]int  // by player key
	playStats map[string]*Stats // from the telemetry stream, by name
}

// Start begins a new match.
func (t *Tracker) Start(session, gameType, level string, at time.Time) {
	t.m = Match{Version: Version, Active: true, Source: "log", SessionID: session, StartedAt: at.UTC(), GameType: gameType, Map: level}
	t.prev, t.roster, t.order = nil, map[string]SummaryPlayer{}, nil
	t.logRoster, t.logOrder, t.logPhases, t.logPlayers, t.roundPlaying, t.pending = map[string]*logPlayer{}, nil, false, false, false, nil
	t.pings, t.playStats = map[string][]int{}, nil
}

// SetTelemetry records whether the telemetry stream is connected.
func (t *Tracker) SetTelemetry(on bool) { t.m.Telemetry = on }

// Active reports whether the tracked match is still running.
func (t *Tracker) Active() bool { return t.m.Active }

// Started reports whether a match was started.
func (t *Tracker) Started() bool { return t.m.SessionID != "" || !t.m.StartedAt.IsZero() }

// Session is the tracked match's session id.
func (t *Tracker) Session() string { return t.m.SessionID }

// SetLog fills in what the log said after the start (game type, level).
func (t *Tracker) SetLog(gameType, level string) {
	if gameType != "" {
		t.m.GameType = gameType
	}
	if level != "" {
		t.m.Map = level
	}
}

// SetAPI records the API's state when it gave no session.
func (t *Tracker) SetAPI(state, errText string) {
	t.m.API, t.m.APIError = state, errText
}

// Match returns the current match.
func (t *Tracker) Match() Match {
	m := t.m
	m.Events = append([]Event(nil), t.m.Events...)
	m.Plays = append([]Event(nil), t.m.Plays...)
	t.logTeams(&m)
	if m.Blue != nil && m.Orange != nil {
		b, o := *m.Blue, *m.Orange
		m.Blue, m.Orange = &b, &o
		t.playTotals(&m)
	}
	return m
}

// Observe takes one /session answer.
func (t *Tracker) Observe(s *Session, now time.Time) {
	now = now.UTC()
	m := &t.m
	m.API, m.APIError, m.Source = APIOn, "", "session_api"
	if m.SessionID == "" {
		m.SessionID = strings.ToLower(s.SessionID)
	}
	if s.MapName != "" && m.Map == "" {
		m.Map = s.MapName
	}
	m.MatchType, m.Private, m.Tournament = s.MatchType, s.PrivateMatch, s.TournamentMatch
	m.Status, m.Clock, m.ClockDisplay = s.GameStatus, s.GameClock, s.GameClockDisplay
	m.Paused, m.PausedBy, m.PauseTimer = "", "", 0
	if ps := pausedState(s); ps != "" && ps != "unpaused" {
		m.Paused = ps
		if s.Pause != nil {
			m.PausedBy = teamColor(s.Pause.PausedRequestedTeam)
			if m.PausedBy == "none" {
				m.PausedBy = ""
			}
			m.PauseTimer = round2(max(s.Pause.PausedTimer, s.Pause.UnpausedTimer))
		}
	}
	switch blueR, orangeR := bool(s.BlueRestart), bool(s.OrangeRestart); {
	case blueR && orangeR:
		m.RestartRequest = "both"
	case blueR:
		m.RestartRequest = "blue"
	case orangeR:
		m.RestartRequest = "orange"
	default:
		m.RestartRequest = ""
	}
	m.RulesChangedBy = s.RulesChangedBy
	if m.RulesChangedBy == "[INVALID]" { // what the game says when nobody changed them
		m.RulesChangedBy = ""
	}
	m.Disc = nil
	if s.Disc != nil && !m.Percent {
		m.Disc = &Disc{Pos: round1s(s.Disc.Position), Bounces: s.Disc.BounceCount}
		m.Bounds = m.Bounds.add(m.Disc.Pos)
	}
	m.Contested = s.Contested != nil && *s.Contested
	m.Percent = isCombat(s.MatchType) || isCombat(m.GameType)
	if s.TotalRoundCount > 0 || s.BlueRoundScore > 0 || s.OrangeRoundScore > 0 {
		m.Rounds = &Rounds{Total: s.TotalRoundCount, Blue: s.BlueRoundScore, Orange: s.OrangeRoundScore}
	} else {
		m.Rounds = nil
	}
	blue, orange, spec := splitTeams(s.Teams)
	m.Blue = team(blue, round2(s.BluePoints))
	m.Orange = team(orange, round2(s.OrangePoints))
	m.Spectators = nil
	if spec != nil {
		m.Spectators = players(spec.Players)
	}
	m.Players = len(m.Blue.Players) + len(m.Orange.Players)
	for _, ps := range [][]Player{m.Blue.Players, m.Orange.Players} {
		for i := range ps {
			p := &ps[i]
			m.Bounds = m.Bounds.add(p.Pos)
			k := orDefault(p.UserID, "name:"+p.Name)
			if t.pings == nil {
				t.pings = map[string][]int{}
			}
			tr := append(t.pings[k], p.Ping)
			if len(tr) > PingTrendMax {
				tr = tr[len(tr)-PingTrendMax:]
			}
			t.pings[k] = tr
			p.PingTrend = append([]int(nil), tr...)
		}
	}
	if s.LastScore != nil && s.LastScore.Team != "" && (s.LastScore.PointAmount > 0 || s.LastScore.PersonScored != "") {
		m.LastGoal = &Goal{Team: teamColor(s.LastScore.Team), Points: s.LastScore.PointAmount, Type: s.LastScore.GoalType,
			Scorer: s.LastScore.PersonScored, Assist: s.LastScore.AssistScored, Speed: plausible(s.LastScore.DiscSpeed, 100), Distance: plausible(s.LastScore.DistanceThrown, 200)}
		if m.LastGoal.Assist == m.LastGoal.Scorer {
			m.LastGoal.Assist = "" // the game names the scorer when nobody assisted
		}
	}
	m.Payload = nil
	if s.PayloadCheckpt != nil || s.PayloadDistance != nil {
		m.Payload = &Payload{Checkpoint: deref(s.PayloadCheckpt), Distance: round2(deref(s.PayloadDistance)), Defenders: deref(s.PayloadDefenders),
			Speed: round2(deref(s.PayloadSpeed)), Multiplier: round2(deref(s.PayloadMult))}
	}
	t.diff(s, now)
	t.remember(m)
	t.prev = s
}

// Finish ends the match and returns its summary; ok is false when no match
// was started.
func (t *Tracker) Finish(now time.Time) (Summary, bool) {
	if !t.Started() {
		return Summary{}, false
	}
	t.flushLeave(now, true)
	m := &t.m
	m.Active, m.EndedAt = false, now.UTC()
	sum := Summary{SessionID: m.SessionID, GameType: m.GameType, Map: m.Map, MatchType: m.MatchType, Private: m.Private, Percent: m.Percent,
		StartedAt: m.StartedAt, EndedAt: m.EndedAt, Duration: int(m.EndedAt.Sub(m.StartedAt).Seconds()), Source: m.Source, Rounds: m.Rounds}
	if m.Blue != nil && m.Orange != nil {
		sum.Blue, sum.Orange = m.Blue.Points, m.Orange.Points
		switch {
		// Combat is won by rounds (its points are the last round's capture
		// progress); arena by rounds when it plays more than one.
		case m.Rounds != nil && (m.Percent || m.Rounds.Total > 1) && m.Rounds.Blue != m.Rounds.Orange:
			sum.Winner = map[bool]string{true: "blue", false: "orange"}[m.Rounds.Blue > m.Rounds.Orange]
		case sum.Blue > sum.Orange:
			sum.Winner = "blue"
		case sum.Orange > sum.Blue:
			sum.Winner = "orange"
		case m.Source == "session_api":
			sum.Winner = "tie"
		}
	}
	for _, k := range t.order {
		sum.Players = append(sum.Players, t.roster[k])
	}
	if len(sum.Players) == 0 {
		sum.Players = t.logSummaryPlayers()
	}
	return sum, true
}

func (t *Tracker) remember(m *Match) {
	add := func(side string, ps []Player) {
		for _, p := range ps {
			k := p.UserID
			if k == "" {
				k = "name:" + p.Name
			}
			if _, ok := t.roster[k]; !ok {
				t.order = append(t.order, k)
			}
			old := t.roster[k]
			sp := SummaryPlayer{Name: p.Name, UserID: p.UserID, Team: side, Stats: p.Stats}
			if side == "spectator" && old.Team != "" && old.Team != "spectator" {
				sp.Team, sp.Stats = old.Team, old.Stats // keep the side they played on
			}
			t.roster[k] = sp
		}
	}
	if t.roster == nil {
		t.roster = map[string]SummaryPlayer{}
	}
	add("blue", m.Blue.Players)
	add("orange", m.Orange.Players)
	add("spectator", m.Spectators)
}

// notable game_status values that become events.
var notable = map[string]string{
	"round_start":       "Round starting",
	"round_over":        "Round over",
	"post_match":        "Match over",
	"sudden_death":      "Sudden death",
	"post_sudden_death": "Sudden death over",
	"pre_match":         "Waiting for players",
}

func (t *Tracker) diff(s *Session, now time.Time) {
	p := t.prev
	if p == nil {
		return // the first answer is the baseline
	}
	clock := s.GameClockDisplay
	add := func(e Event) {
		e.At, e.Clock = now, clock
		t.addEvent(e)
	}
	goal := func(side string, pts float64) {
		e := Event{Type: EventGoal, Team: side, Text: fmt.Sprintf("%s scores %g", title(side), pts)}
		if ls := s.LastScore; ls != nil && teamColor(ls.Team) == side {
			e.Player = ls.PersonScored
			parts := []string{}
			if ls.PersonScored != "" {
				parts = append(parts, ls.PersonScored)
			}
			if ls.AssistScored != "" && ls.AssistScored != ls.PersonScored {
				parts = append(parts, "assist "+ls.AssistScored)
			}
			if v := plausible(ls.DiscSpeed, 100); v > 0 {
				parts = append(parts, fmt.Sprintf("%.1f m/s", v))
			}
			if v := plausible(ls.DistanceThrown, 200); v > 0 {
				parts = append(parts, fmt.Sprintf("%.1f m", v))
			}
			if len(parts) > 0 {
				e.Text += " (" + strings.Join(parts, ", ") + ")"
			}
		}
		add(e)
	}
	// Combat's points are capture progress and rise all the time: no goals there.
	if !t.m.Percent {
		if d := s.BluePoints - p.BluePoints; d > 0 {
			goal("blue", d)
		}
		if d := s.OrangePoints - p.OrangePoints; d > 0 {
			goal("orange", d)
		}
	}
	if s.BlueRoundScore > p.BlueRoundScore {
		add(Event{Type: EventRound, Team: "blue", Text: fmt.Sprintf("Blue wins the round (%d–%d)", s.BlueRoundScore, s.OrangeRoundScore)})
	}
	if s.OrangeRoundScore > p.OrangeRoundScore {
		add(Event{Type: EventRound, Team: "orange", Text: fmt.Sprintf("Orange wins the round (%d–%d)", s.BlueRoundScore, s.OrangeRoundScore)})
	}
	if s.GameStatus != p.GameStatus && !t.logPhases {
		if text, ok := notable[s.GameStatus]; ok {
			add(Event{Type: EventStatus, Text: text})
		}
	}
	was, now2 := pausedState(p), pausedState(s)
	if was != now2 {
		switch {
		case now2 == "paused" && s.Pause != nil:
			add(Event{Type: EventPause, Team: teamColor(s.Pause.PausedRequestedTeam), Text: "Paused by " + orDefault(teamColor(s.Pause.PausedRequestedTeam), "a team")})
		case now2 == "" && was == "paused" && s.Pause != nil:
			add(Event{Type: EventPause, Team: teamColor(s.Pause.UnpausedTeam), Text: "Unpaused"})
		}
	}
	t.sessionPlays(p, s, now)
	if t.logPlayers {
		return
	}
	before, after := sides(p), sides(s)
	for _, k := range sortedKeys(after) {
		a := after[k]
		b, ok := before[k]
		switch {
		case !ok:
			add(Event{Type: EventJoin, Team: a.side, Player: a.name, Text: fmt.Sprintf("%s joined %s", a.name, sideName(a.side))})
		case b.side != a.side:
			add(Event{Type: EventSwitch, Team: a.side, Player: a.name, Text: fmt.Sprintf("%s moved to %s", a.name, sideName(a.side))})
		}
	}
	for _, k := range sortedKeys(before) {
		if _, ok := after[k]; !ok {
			b := before[k]
			add(Event{Type: EventLeave, Team: b.side, Player: b.name, Text: b.name + " left"})
		}
	}
}

type seat struct{ name, side string }

func sides(s *Session) map[string]seat {
	out := map[string]seat{}
	blue, orange, spec := splitTeams(s.Teams)
	for side, tm := range map[string]*APITeam{"blue": blue, "orange": orange, "spectator": spec} {
		if tm == nil {
			continue
		}
		for _, p := range tm.Players {
			k := strconv.FormatUint(p.UserID, 10)
			if p.UserID == 0 {
				k = "name:" + p.Name
			}
			out[k] = seat{p.Name, side}
		}
	}
	return out
}

func sortedKeys(m map[string]seat) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// pausedState is "" when the game runs (combat says "none", arena "unpaused").
func pausedState(s *Session) string {
	if s.Pause == nil || s.Pause.PausedState == "none" || s.Pause.PausedState == "unpaused" {
		return ""
	}
	return s.Pause.PausedState
}

func isCombat(s string) bool { return strings.Contains(strings.ToLower(s), "combat") }

// splitTeams finds blue, orange and the spectators by name, else by position.
func splitTeams(ts []APITeam) (blue, orange, spec *APITeam) {
	for i := range ts {
		switch name := strings.ToUpper(ts[i].Team); {
		case strings.Contains(name, "BLUE"):
			blue = &ts[i]
		case strings.Contains(name, "ORANGE"):
			orange = &ts[i]
		case strings.Contains(name, "SPECTATOR"):
			spec = &ts[i]
		}
	}
	at := func(i int) *APITeam {
		if i < len(ts) {
			return &ts[i]
		}
		return nil
	}
	if blue == nil && orange == nil && spec == nil {
		blue, orange, spec = at(0), at(1), at(2)
	}
	return blue, orange, spec
}

func team(t *APITeam, points float64) *Team {
	out := &Team{Points: points, Players: []Player{}}
	if t == nil {
		return out
	}
	out.Possession, out.Stats = t.Possession, t.Stats
	out.Players = players(t.Players)
	return out
}

func players(ps []APIPlayer) []Player {
	out := make([]Player, 0, len(ps))
	for _, p := range ps {
		q := Player{Name: p.Name, Slot: p.PlayerID, Number: p.Number, Level: p.Level, Ping: p.Ping,
			PacketLoss: round2(p.PacketLossRatio), Stunned: p.Stunned, Possession: p.Possession,
			Blocking: p.Blocking, Emote: p.Emote, Arm: p.Arm,
			Weapon: p.Weapon, Ordnance: p.Ordnance, TacMod: p.TacMod, Stats: p.Stats}
		if p.Head != nil {
			q.Pos = round1s(p.Head.Position)
		}
		if p.UserID != 0 {
			q.UserID = strconv.FormatUint(p.UserID, 10)
		}
		out = append(out, q)
	}
	return out
}

func teamColor(s string) string {
	switch u := strings.ToUpper(s); {
	case strings.Contains(u, "BLUE"):
		return "blue"
	case strings.Contains(u, "ORANGE"):
		return "orange"
	}
	return strings.ToLower(s)
}

func sideName(side string) string {
	switch side {
	case "spectator":
		return "the spectators"
	case "social":
		return "the lobby"
	}
	return title(side)
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// plausible is f rounded when it lies in (0, limit], else 0: the game
// sometimes reports uninitialised numbers (−9.7e23 m/s) for a goal.
func plausible(f, limit float64) float64 {
	if f <= 0 || f > limit || math.IsNaN(f) {
		return 0
	}
	return round2(f)
}

// round1s rounds a position to 10 cm; nil for none.
func round1s(v []float64) []float64 {
	if len(v) == 0 {
		return nil
	}
	out := make([]float64, len(v))
	for i, f := range v {
		out[i] = math.Round(f*10) / 10
	}
	return out
}

func marshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func deref[T int | float64](p *T) T {
	if p == nil {
		return 0
	}
	return *p
}

// WriteMatch stores m as home's match.json.
func WriteMatch(home string, m Match) error {
	m.Version, m.UpdatedAt = Version, time.Now().UTC()
	b, err := marshal(m)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(home, FileName), b)
}

// ReadMatch loads home's match.json.
func ReadMatch(home string) (Match, error) {
	var m Match
	err := readJSON(filepath.Join(home, FileName), &m)
	return m, err
}

// AppendHistory puts s first in home's match-history.json, keeping HistoryMax.
func AppendHistory(home string, s Summary) error {
	h, _ := ReadHistory(home)
	h.Version = Version
	out := []Summary{s}
	for _, old := range h.Matches {
		if old.SessionID != s.SessionID || s.SessionID == "" {
			out = append(out, old)
		}
	}
	if len(out) > HistoryMax {
		out = out[:HistoryMax]
	}
	h.Matches = out
	b, err := marshal(h)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(home, HistoryFile), b)
}

// ReadHistory loads home's match-history.json.
func ReadHistory(home string) (History, error) {
	var h History
	err := readJSON(filepath.Join(home, HistoryFile), &h)
	return h, err
}

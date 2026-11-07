package match

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Note kinds: what the game's log said about the match. They work with the
// session API off, and in combat, whose /session has no clock or status.
const (
	NotePhase = "phase" // the match's state machine entered Phase
	NoteTeam  = "team"  // the server put Player on Team (join or switch)
	NoteSpawn = "spawn" // Player (Account) spawned
	NoteLeave = "leave" // Player disconnected
	NotePing  = "ping"  // the server's ping stats of the player who just left
	NoteGone  = "gone"  // the peer of the player who just left was dropped (Reason)
	// NotePlay comes from nevr-runtime's telemetry stream: Player did Play
	// (stun, save, steal, …) for the Total-th time; Text describes it.
	NotePlay = "play"
)

// PlaysMax is how many plays Match.Plays keeps.
const PlaysMax = 40

// Phases in Match.Phase.
const (
	PhasePreMatch  = "pre_match"
	PhaseRound     = "round_start"
	PhasePlaying   = "playing"
	PhaseScored    = "point_scored"
	PhaseRoundOver = "round_over"
	PhasePostMatch = "post_match"
	PhaseReturning = "returning"
)

// EventPhase is an event from the game's state machine.
const EventPhase = "phase"

// Leave reasons in Note.Reason.
const (
	LeaveLeft = "left"
	LeaveLost = "connection lost"
)

// HiddenName is the name the game logs for a player it hides (team 4).
const HiddenName = "..."

// Note is one log line that matters for the match.
type Note struct {
	Kind     string
	At       time.Time
	Phase    string // NotePhase: pre_match, round_start, main_loop, point_score, round_over, post_match_celebration, …
	Player   string
	Team     string // NoteTeam: blue, orange, spectator, social; "" hidden
	Account  string // NoteSpawn: OVR-ORG-123…
	GameOver bool   // NoteLeave: after the match
	PingMs   float64
	PingMax  float64
	Reason   string // NoteGone: LeaveLeft or LeaveLost
	Play     string // NotePlay: stun, save, steal, interception, block, pass, shot, assist, goal
	Total    int    // NotePlay
	Text     string // NotePlay
}

// TeamName is a team number from the game's log as blue, orange, spectator
// or social (everyone in a social lobby); "" for hidden players (4).
func TeamName(n int) string {
	switch n {
	case 0:
		return "blue"
	case 1:
		return "orange"
	case 2:
		return "spectator"
	case 3:
		return "social"
	}
	return ""
}

// IsLobby reports whether a game type is a social lobby rather than a match.
func IsLobby(gameType string) bool { return strings.HasPrefix(strings.ToLower(gameType), "social") }

type logPlayer struct {
	Name, Team, Account string
	Present             bool
}

type pendingLeave struct {
	at              time.Time
	name, side      string
	gameOver        bool
	pingMs, pingMax float64
	reason          string
}

// leaveWait is how long a leave waits for its ping stats and reason.
const leaveWait = 2 * time.Second

// Note takes one note from the game's log.
func (t *Tracker) Note(n Note) {
	if n.At.IsZero() {
		n.At = time.Now()
	}
	n.At = n.At.UTC()
	if t.logRoster == nil {
		t.logRoster = map[string]*logPlayer{}
	}
	switch n.Kind {
	case NotePhase:
		t.phase(n)
	case NoteTeam:
		if n.Player == "" || n.Player == HiddenName || n.Team == "" {
			return
		}
		t.flushLeave(n.At, true)
		t.logPlayers = true
		p, ok := t.logRoster[n.Player]
		switch {
		case !ok:
			t.logRoster[n.Player] = &logPlayer{Name: n.Player, Team: n.Team, Present: true}
			t.logOrder = append(t.logOrder, n.Player)
			t.addEvent(Event{At: n.At, Type: EventJoin, Team: n.Team, Player: n.Player, Text: fmt.Sprintf("%s joined %s", n.Player, sideName(n.Team))})
		case !p.Present:
			p.Present = true
			text := fmt.Sprintf("%s rejoined %s", n.Player, sideName(n.Team))
			p.Team = n.Team
			t.addEvent(Event{At: n.At, Type: EventJoin, Team: n.Team, Player: n.Player, Text: text})
		case p.Team != n.Team:
			p.Team = n.Team
			t.addEvent(Event{At: n.At, Type: EventSwitch, Team: n.Team, Player: n.Player, Text: fmt.Sprintf("%s moved to %s", n.Player, sideName(n.Team))})
		}
	case NoteSpawn:
		if p, ok := t.logRoster[n.Player]; ok {
			p.Account = n.Account
		}
	case NoteLeave:
		t.flushLeave(n.At, true)
		side := ""
		if p, ok := t.logRoster[n.Player]; ok {
			p.Present, side = false, p.Team
		} else {
			return // a hidden or unknown player
		}
		t.pending = &pendingLeave{at: n.At, name: n.Player, side: side, gameOver: n.GameOver}
	case NotePing:
		if t.pending != nil && t.pending.pingMs == 0 {
			t.pending.pingMs, t.pending.pingMax = n.PingMs, n.PingMax
		}
	case NoteGone:
		if t.pending != nil {
			t.pending.reason = n.Reason
			t.flushLeave(n.At, true)
		}
	case NotePlay:
		t.play(n)
	}
}

// Flush writes out a leave whose details did not come within leaveWait.
func (t *Tracker) Flush(now time.Time) { t.flushLeave(now, false) }

func (t *Tracker) flushLeave(now time.Time, force bool) {
	p := t.pending
	if p == nil || (!force && now.Sub(p.at) < leaveWait) {
		return
	}
	t.pending = nil
	if p.gameOver {
		return // everybody leaves after the match
	}
	text := p.name + " left"
	if p.reason == LeaveLost {
		text = p.name + " lost the connection"
	}
	if p.pingMs > 0 {
		text += fmt.Sprintf(" (ping %.0f ms, max %.0f ms)", p.pingMs, p.pingMax)
	}
	t.addEvent(Event{At: p.at, Type: EventLeave, Team: p.side, Player: p.name, Text: text})
}

func (t *Tracker) phase(n Note) {
	m := &t.m
	t.logPhases = true
	apiOn := m.API == APIOn
	set := func(p string) { m.Phase, m.PhaseSince = p, n.At }
	switch n.Phase {
	case "pre_match":
		set(PhasePreMatch)
		t.addEvent(Event{At: n.At, Type: EventPhase, Text: "Waiting for players"})
	case "round_start":
		m.Round++
		t.roundPlaying = false
		set(PhaseRound)
		t.addEvent(Event{At: n.At, Type: EventPhase, Text: fmt.Sprintf("Round %d starting", m.Round)})
	case "main_loop":
		set(PhasePlaying)
		if !t.roundPlaying {
			t.roundPlaying, m.RoundStartedAt = true, n.At
		}
	case "point_score":
		set(PhaseScored)
		if !apiOn {
			t.addEvent(Event{At: n.At, Type: EventGoal, Text: "Point scored"})
		}
	case "round_over":
		set(PhaseRoundOver)
		t.roundPlaying = false
		if !apiOn {
			t.addEvent(Event{At: n.At, Type: EventPhase, Text: fmt.Sprintf("Round %d over", max(m.Round, 1))})
		}
	case "post_match_celebration":
		set(PhasePostMatch)
		t.addEvent(Event{At: n.At, Type: EventPhase, Text: "Match over"})
	case "waiting_to_return_to_lobby":
		set(PhaseReturning)
	}
}

// addEvent puts e first in the match's events, keeping EventsMax.
func (t *Tracker) addEvent(e Event) {
	if e.Clock == "" && t.m.API == APIOn {
		e.Clock = t.m.ClockDisplay
	}
	t.m.Events = append([]Event{e}, t.m.Events...)
	if len(t.m.Events) > EventsMax {
		t.m.Events = t.m.Events[:EventsMax]
	}
}

// logTeams fills a match without session API data with the players the log named.
func (t *Tracker) logTeams(m *Match) {
	if m.Source != "log" || len(t.logOrder) == 0 {
		return
	}
	lobby := IsLobby(m.GameType)
	m.Blue, m.Orange, m.Spectators = &Team{Players: []Player{}}, &Team{Players: []Player{}}, nil
	for _, name := range t.logOrder {
		p := t.logRoster[name]
		if !p.Present {
			continue
		}
		q := Player{Name: p.Name, UserID: accountNumber(p.Account)}
		switch {
		case lobby: // a lobby has no teams: everyone is a spectator
			m.Spectators = append(m.Spectators, q)
		case p.Team == "blue":
			m.Blue.Players = append(m.Blue.Players, q)
		case p.Team == "orange":
			m.Orange.Players = append(m.Orange.Players, q)
		default:
			m.Spectators = append(m.Spectators, q)
		}
	}
	m.Players = len(m.Blue.Players) + len(m.Orange.Players)
	if lobby {
		m.Blue, m.Orange, m.Players = nil, nil, len(m.Spectators)
	}
}

// logSummaryPlayers is the summary's player list when the API gave none.
func (t *Tracker) logSummaryPlayers() []SummaryPlayer {
	var out []SummaryPlayer
	for _, name := range t.logOrder {
		p := t.logRoster[name]
		out = append(out, SummaryPlayer{Name: p.Name, UserID: accountNumber(p.Account), Team: orDefault(p.Team, "spectator")})
	}
	return out
}

// accountNumber is the number in a platform account (OVR-ORG-123 → 123),
// which is what /session calls userid.
func accountNumber(acct string) string {
	if i := strings.LastIndexByte(acct, '-'); i >= 0 {
		return acct[i+1:]
	}
	return acct
}

// play records a player's action from the telemetry stream: passes only
// count; the rest also go into the play-by-play.
func (t *Tracker) play(n Note) {
	if n.Player == "" {
		return
	}
	if t.playStats == nil {
		t.playStats = map[string]*Stats{}
	}
	st := t.playStats[n.Player]
	if st == nil {
		st = &Stats{}
		t.playStats[n.Player] = st
	}
	set := func(p *int) {
		if n.Total > *p {
			*p = n.Total
		} else {
			*p++
		}
	}
	switch n.Play {
	case "stun":
		set(&st.Stuns)
	case "save":
		set(&st.Saves)
	case "steal":
		set(&st.Steals)
	case "interception":
		set(&st.Interceptions)
	case "block":
		set(&st.Blocks)
	case "pass":
		set(&st.Passes)
		return
	case "shot":
		set(&st.ShotsTaken)
	case "assist":
		set(&st.Assists)
		return // the goal event names the assist
	case "goal":
		set(&st.Goals)
		return // goals come from /session or the log
	}
	e := Event{At: n.At, Type: EventPlay, Team: n.Team, Player: n.Player, Text: n.Text}
	if t.m.API == APIOn {
		e.Clock = t.m.ClockDisplay
	}
	t.addPlay(e)
}

// addPlay puts e first in the match's plays, keeping PlaysMax.
func (t *Tracker) addPlay(e Event) {
	t.m.Plays = append([]Event{e}, t.m.Plays...)
	if len(t.m.Plays) > PlaysMax {
		t.m.Plays = t.m.Plays[:PlaysMax]
	}
}

// sessionPlays turns the rise of a player's /session counters between two
// answers into plays (stuns, saves, steals, …). Goals and assists are goal
// events already; passes and catches only count.
func (t *Tracker) sessionPlays(prev, s *Session, now time.Time) {
	old := map[string]*Stats{}
	for _, tm := range prev.Teams {
		for _, p := range tm.Players {
			if p.Stats != nil {
				old[playerKey(p)] = p.Stats
			}
		}
	}
	blue, orange, _ := splitTeams(s.Teams)
	for side, tm := range map[string]*APITeam{"blue": blue, "orange": orange} {
		if tm == nil {
			continue
		}
		for _, p := range tm.Players {
			b, ok := old[playerKey(p)]
			if !ok || p.Stats == nil {
				continue // a player's first answer is their baseline
			}
			a := p.Stats
			for _, c := range []struct {
				now, was int
				text     string
			}{
				{a.Stuns, b.Stuns, "%s stuns (%d)"},
				{a.Saves, b.Saves, "%s saves (%d)"},
				{a.Steals, b.Steals, "%s steals the disc (%d)"},
				{a.Interceptions, b.Interceptions, "%s intercepts (%d)"},
				{a.Blocks, b.Blocks, "%s blocks (%d)"},
				{a.ShotsTaken, b.ShotsTaken, "%s shoots (%d)"},
			} {
				if c.now > c.was {
					t.addPlay(Event{At: now, Clock: s.GameClockDisplay, Type: EventPlay, Team: side, Player: p.Name, Text: fmt.Sprintf(c.text, p.Name, c.now)})
				}
			}
		}
	}
}

func playerKey(p APIPlayer) string {
	if p.UserID == 0 {
		return "name:" + p.Name
	}
	return strconv.FormatUint(p.UserID, 10)
}

// playTotals gives players the stream's counts where /session has none
// (combat, or the API off).
func (t *Tracker) playTotals(m *Match) {
	if len(t.playStats) == 0 {
		return
	}
	for _, tm := range []*Team{m.Blue, m.Orange} {
		if tm == nil {
			continue
		}
		ps := make([]Player, len(tm.Players))
		copy(ps, tm.Players)
		for i := range ps {
			if ps[i].Stats == nil {
				if st := t.playStats[ps[i].Name]; st != nil {
					c := *st
					ps[i].Stats = &c
				}
			}
		}
		tm.Players = ps
	}
}

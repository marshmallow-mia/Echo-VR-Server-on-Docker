package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/telemetry"
)

// applyAPISetting writes the instance's session API switch into the game's
// settings before a start and returns it. A failure is logged, not fatal: the
// server runs without match stats.
func (s *Supervisor) applyAPISetting() bool {
	on := match.LoadSettings(s.o.Home).APIOn()
	path := game.SettingsPath(s.prefix(), s.o.User)
	if _, err := game.SetAPIAccess(path, on); err != nil {
		s.logf("match stats: cannot switch the game's API %s: %v", status.OnOff(on), err)
		if cur, rerr := game.APIAccess(path); rerr == nil {
			return cur
		}
		return false
	}
	return on
}

// setAPI stores the switch. An idle game restarts at once to pick it up; one
// in a match (or signing in) picks it up at its next start.
func (s *Supervisor) setAPI(on bool) {
	want := status.OnOff(on)
	if err := match.SetAPI(s.o.Home, on); err != nil {
		s.logf("match stats API: cannot save the setting: %v", err)
		return
	}
	st := s.Status()
	switch {
	case st.PID == 0:
		s.update(func(st *status.Status) { st.SessionAPINext = want })
		s.logf("match stats API %s from the next start", want)
	case st.SessionAPI == want:
		s.update(func(st *status.Status) { st.SessionAPINext = "" })
		s.logf("match stats API is already %s", want)
	case st.State == status.StateRegistered:
		s.update(func(st *status.Status) { st.SessionAPINext = want })
		s.logf("match stats API %s: restarting the idle game to apply it", want)
		s.Restart()
	default:
		s.update(func(st *status.Status) { st.SessionAPINext = want })
		s.logf("match stats API %s after this match (the game reads the setting when it starts)", want)
	}
}

// note hands a log line to the match tracker; it never blocks the log reader.
func (s *Supervisor) note(n match.Note) {
	n.At = time.Now()
	select {
	case s.notes <- n:
	default:
	}
}

// matchLoop follows matches: while one runs it asks the game's /session every
// MatchPoll and keeps match.json up to date, and between those every LivePoll
// for live.json (the map's positions); when it ends the match goes into
// match-history.json and live.json goes away. In a social lobby, where the game
// refuses /session, live.json comes from the telemetry stream's newest frame.
func (s *Supervisor) matchLoop(ctx context.Context) {
	home := s.o.Home
	// A match.json left active by a container that went away is over.
	if m, err := match.ReadMatch(home); err == nil && m.Active {
		m.Active, m.EndedAt = false, m.UpdatedAt
		_ = match.WriteMatch(home, m)
	}
	var (
		tr        match.Tracker
		client    match.Client
		last      []byte
		lastWrite time.Time
	)
	write := func(force bool) {
		m := tr.Match()
		b, _ := json.Marshal(m)
		if !force && bytes.Equal(b, last) && time.Since(lastWrite) < s.o.Heartbeat {
			return
		}
		if err := match.WriteMatch(home, m); err != nil {
			s.logf("match stats: cannot write %s: %v", match.FileName, err)
			return
		}
		last, lastWrite = b, time.Now()
	}
	finish := func() {
		sum, ok := tr.Finish(time.Now())
		if !ok {
			return
		}
		write(true)
		if match.IsLobby(sum.GameType) {
			return // a social lobby is no match
		}
		if err := match.AppendHistory(home, sum); err != nil {
			s.logf("match stats: cannot write %s: %v", match.HistoryFile, err)
		}
	}
	// follow starts and ends the tracked match to match st; it reports
	// whether a match runs.
	follow := func(st status.Status) bool {
		// A session that ended stays in Session (StateStopping) until the game exits.
		inMatch := st.Session != "" && (st.State == status.StateInSession || st.State == status.StateStopping)
		if !inMatch {
			if tr.Active() {
				finish()
			}
			return false
		}
		if !tr.Active() || tr.Session() != st.Session {
			if tr.Active() {
				finish()
			}
			tr.Start(st.Session, st.GameType, st.Map, st.Since)
		}
		tr.SetLog(st.GameType, st.Map)
		tr.SetTelemetry(st.TelemetryConnected)
		return true
	}
	// live.json carries only the map's positions; it is written every
	// LivePoll while /session answers, match.json every MatchPoll.
	liveOn := false
	clearLive := func() {
		if liveOn {
			_ = match.RemoveLive(home)
			liveOn = false
		}
	}
	_ = match.RemoveLive(home) // left by a container that went away
	defer clearLive()
	putLive := func(l match.Live) {
		if err := match.WriteLive(home, l); err != nil {
			s.logf("match stats: cannot write %s: %v", match.LiveFile, err)
			return
		}
		liveOn = true
	}
	writeLive := func(sess *match.Session, now time.Time) {
		if s.o.LivePoll > 0 {
			putLive(match.LiveFrom(sess, now))
		}
	}
	// A lobby's live.json follows the stream: a frame is written once, and
	// none newer than lobbyStale means the stream stopped or sends no positions.
	var lobbyAt time.Time
	writeLobby := func(st status.Status, now time.Time) {
		if s.o.LivePoll <= 0 {
			return
		}
		l, ok := lobbyLive(s.latest.Get(), st, now)
		if !ok {
			clearLive()
			lobbyAt = time.Time{}
			return
		}
		if l.AtMS != lobbyAt.UnixMilli() {
			putLive(l)
			lobbyAt = time.UnixMilli(l.AtMS)
		}
	}
	every := s.o.MatchPoll
	if s.o.LivePoll > 0 && s.o.LivePoll < every {
		every = s.o.LivePoll
	}
	var lastFull time.Time
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if tr.Active() {
				finish()
			}
			return
		case n := <-s.notes:
			if follow(s.Status()) {
				tr.Note(n)
				write(false)
			}
			continue
		case <-t.C:
		}
		st := s.Status()
		if !follow(st) {
			clearLive()
			continue
		}
		now := time.Now()
		// A full round (the tracker, match.json) once per MatchPoll; the
		// ticks between only refresh live.json.
		full := now.Sub(lastFull) >= s.o.MatchPoll-every/2
		if match.IsLobby(st.GameType) && st.State != status.StateStopping {
			writeLobby(st, now)
			if !full {
				continue
			}
		}
		fetch := st.State != status.StateStopping && !match.IsLobby(st.GameType) &&
			st.SessionAPI == status.OnOff(true) && st.HTTPPort != 0
		if !full {
			if !fetch {
				continue
			}
			fctx, cancel := context.WithTimeout(ctx, every*4)
			sess, err := client.Fetch(fctx, st.HTTPPort)
			cancel()
			if err == nil {
				writeLive(sess, time.Now())
			} else {
				clearLive()
			}
			continue
		}
		lastFull = now
		tr.Flush(now)
		switch {
		case st.State == status.StateStopping:
			// The match is over and the game on its way out: keep the last answer.
			clearLive()
		case match.IsLobby(st.GameType):
			// A social lobby has no match for /session to describe; the log names its players.
		case st.SessionAPI != status.OnOff(true):
			tr.SetAPI(match.APIOff, "")
			clearLive()
		case st.HTTPPort == 0:
			tr.SetAPI(match.APIWaiting, "the game's HTTP API has not started yet")
		default:
			fctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
			sess, err := client.Fetch(fctx, st.HTTPPort)
			cancel()
			switch {
			case err == nil:
				tr.Observe(sess, time.Now())
				writeLive(sess, time.Now())
			case errors.Is(err, match.ErrDisabled):
				tr.SetAPI(match.APIDisabled, "the running game has its API setting off; it is switched on at the next start")
				clearLive()
			case errors.Is(err, match.ErrNoSession):
				tr.SetAPI(match.APIWaiting, "no session yet")
				clearLive()
			default:
				tr.SetAPI(match.APIUnavailable, err.Error())
				clearLive()
			}
		}
		write(false)
	}
}

// lobbyStale is how old the stream's newest frame may be for the lobby's map.
const lobbyStale = 2 * time.Second

// lobbyLive is the lobby's live.json from the stream's newest frame f: only
// when f is of this session (a header without a session id is taken as it),
// recent, and has at least one player with a position.
func lobbyLive(f telemetry.Frame, st status.Status, now time.Time) (match.Live, bool) {
	if f.At.IsZero() || now.Sub(f.At) > lobbyStale || len(f.Seats) == 0 {
		return match.Live{}, false
	}
	if f.Session != "" && !strings.EqualFold(f.Session, st.Session) {
		return match.Live{}, false
	}
	l := match.Live{SessionID: strings.ToLower(st.Session), AtMS: f.At.UnixMilli(), Map: st.Map, Players: []match.LivePlayer{}}
	if l.Map == "" {
		l.Map = f.Map
	}
	for _, p := range f.Seats {
		if p.Side == "spectator" {
			continue
		}
		l.Players = append(l.Players, match.LivePlayer{Name: p.Name, Side: p.Side, Pos: []float64{p.Pos[0], p.Pos[1], p.Pos[2]}})
	}
	return l, len(l.Players) > 0
}

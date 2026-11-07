package match

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LiveFile is the instance's live positions file: what the panel's map
// fetches several times a second, apart from match.json's slower update.
const LiveFile = "live.json"

// Live is one /session answer reduced to what a map draws.
type Live struct {
	SessionID string       `json:"session_id"`
	AtMS      int64        `json:"at_ms"` // when the supervisor read it, Unix milliseconds
	Map       string       `json:"map,omitempty"`
	Disc      []float64    `json:"disc,omitempty"` // [x, y, z] in metres; arena only
	Players   []LivePlayer `json:"players"`
}

// LivePlayer is one player's head on the map.
type LivePlayer struct {
	Name       string    `json:"name"`
	Side       string    `json:"side"` // blue or orange; social for a lobby's players
	Pos        []float64 `json:"pos"`  // [x, y, z] in metres (y is up)
	Stunned    bool      `json:"stunned,omitempty"`
	Possession bool      `json:"possession,omitempty"`
}

// LiveFrom reduces s to the map's view. Players without a head position
// (and spectators) are left out.
func LiveFrom(s *Session, now time.Time) Live {
	l := Live{SessionID: strings.ToLower(s.SessionID), AtMS: now.UnixMilli(), Map: s.MapName, Players: []LivePlayer{}}
	if s.Disc != nil && !isCombat(s.MatchType) {
		l.Disc = round1s(s.Disc.Position)
		if len(l.Disc) != 3 {
			l.Disc = nil
		}
	}
	blue, orange, _ := splitTeams(s.Teams)
	for _, t := range []struct {
		side string
		team *APITeam
	}{{"blue", blue}, {"orange", orange}} {
		if t.team == nil {
			continue
		}
		for _, p := range t.team.Players {
			if p.Head == nil || len(p.Head.Position) != 3 {
				continue
			}
			l.Players = append(l.Players, LivePlayer{Name: p.Name, Side: t.side, Pos: round1s(p.Head.Position),
				Stunned: p.Stunned, Possession: p.Possession})
		}
	}
	return l
}

// WriteLive stores l as home's live.json. It is written compact: it changes
// several times a second.
func WriteLive(home string, l Live) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(home, LiveFile), b)
}

// ReadLive loads home's live.json.
func ReadLive(home string) (Live, error) {
	var l Live
	err := readJSON(filepath.Join(home, LiveFile), &l)
	return l, err
}

// RemoveLive deletes home's live.json; a missing one is fine.
func RemoveLive(home string) error {
	if err := os.Remove(filepath.Join(home, LiveFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

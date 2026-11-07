package telemetry

import (
	"math"
	"strings"
	"sync"
	"time"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
)

// Latest keeps the newest frame's player positions for the live map. In a
// social lobby the game's /session answers nothing (err -6), so the stream is
// the only source of where players are; nEVR sends lobby players from
// nevr-runtime #2306 on (team 3, ROLE_SOCIAL_PARTICIPANT).
//
// A Decoder fills it on the connection's goroutine; the match loop reads it.
type Latest struct {
	mu       sync.Mutex
	session  string
	mapName  string
	gameType string
	at       time.Time
	seats    []Seat
}

// Seat is one player's head in the newest frame.
type Seat struct {
	Name string
	Side string     // blue, orange, spectator or social; "" when the stream named none
	Pos  [3]float64 // metres, y up, rounded to 0.1 m
}

// Frame is a copy of what Latest holds.
type Frame struct {
	Session  string // the header's session id
	Map      string // the header's map name
	GameType string // the header's game type
	At       time.Time
	Seats    []Seat
}

// Get returns the newest frame; At is zero before the first frame of the
// current capture.
func (l *Latest) Get() Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Frame{Session: l.session, Map: l.mapName, GameType: l.gameType, At: l.at, Seats: append([]Seat(nil), l.seats...)}
}

// start forgets the last capture's frames: a header begins a new one.
func (l *Latest) start(h *tv2.CaptureHeader) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session, l.mapName, l.gameType = "", "", h.GetGameType()
	if a := h.GetEchoArena(); a != nil {
		l.session, l.mapName = a.GetSessionId(), a.GetMapName()
	}
	l.at, l.seats = time.Time{}, nil
}

// frame stores f's players that have a head position. A player's first frames
// after joining carry zeros until the game has its body; those are left out.
func (l *Latest) frame(f *tv2.EchoArenaFrame, seatOf func(int32) seat, now time.Time) {
	seats := make([]Seat, 0, len(f.GetPlayers()))
	for _, p := range f.GetPlayers() {
		pos := p.GetHead().GetPosition()
		x, y, z := float64(pos.GetX()), float64(pos.GetY()), float64(pos.GetZ())
		if x == 0 && y == 0 && z == 0 {
			continue
		}
		s := seatOf(p.GetSlot())
		seats = append(seats, Seat{Name: s.name, Side: s.team, Pos: [3]float64{round1(x), round1(y), round1(z)}})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.HasPrefix(l.gameType, "social") {
		// The lobby's players are all one crowd (team 3); a runtime before
		// nevr-runtime #2306 names no role for them.
		for i := range seats {
			if seats[i].Side == "" {
				seats[i].Side = "social"
			}
		}
	}
	l.at, l.seats = now, seats
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

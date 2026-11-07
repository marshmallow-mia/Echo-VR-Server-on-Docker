package telemetry

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"

	spv1 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/spatial/v1"
	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

func head(x, y, z float32) *spv1.Pose { return &spv1.Pose{Position: &spv1.Vec3{X: x, Y: y, Z: z}} }

func lobbyHeader(session string) *tv2.Envelope {
	return &tv2.Envelope{Message: &tv2.Envelope_Header{Header: &tv2.CaptureHeader{GameType: "social_2.0",
		GameHeader: &tv2.CaptureHeader_EchoArena{EchoArena: &tv2.EchoArenaHeader{SessionId: session, MapName: "mpl_lobby_b2"}}}}}
}

func arenaFrame(events []*tv2.EchoEvent, players ...*tv2.PlayerState) *tv2.Envelope {
	return &tv2.Envelope{Message: &tv2.Envelope_Frame{Frame: &tv2.Frame{Payload: &tv2.Frame_EchoArena{
		EchoArena: &tv2.EchoArenaFrame{Events: events, Players: players}}}}}
}

func feed(t *testing.T, d *Decoder, ms ...proto.Message) {
	t.Helper()
	for _, m := range ms {
		b, _ := proto.Marshal(m)
		if err := d.Envelope(b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLatestKeepsALobbysPositions(t *testing.T) {
	l := &Latest{}
	d := Decoder{Latest: l}
	joins := []*tv2.EchoEvent{
		{Event: &tv2.EchoEvent_PlayerJoined{PlayerJoined: &tv2.PlayerJoined{Slot: 0, DisplayName: "Ava", Role: tv2.Role_ROLE_SOCIAL_PARTICIPANT}}},
		{Event: &tv2.EchoEvent_PlayerJoined{PlayerJoined: &tv2.PlayerJoined{Slot: 3, DisplayName: "Bo", Role: tv2.Role_ROLE_SOCIAL_PARTICIPANT}}},
		{Event: &tv2.EchoEvent_PlayerJoined{PlayerJoined: &tv2.PlayerJoined{Slot: 5, DisplayName: "Cy"}}}, // a runtime before #2306: no role
	}
	if !l.Get().At.IsZero() {
		t.Fatal("a frame before any")
	}
	feed(t, &d, lobbyHeader("4C23-A"), arenaFrame(joins,
		&tv2.PlayerState{Slot: 0, Head: head(-12.04, 1.6, 3.06)},
		&tv2.PlayerState{Slot: 3, Head: head(0, 0, 0)}, // just joined: no body yet
		&tv2.PlayerState{Slot: 5, Head: head(40, 2, -20)},
		&tv2.PlayerState{Slot: 7, Head: head(1, 1, 1)}, // a slot the stream never named
	))
	f := l.Get()
	if f.Session != "4C23-A" || f.Map != "mpl_lobby_b2" || f.GameType != "social_2.0" || f.At.IsZero() {
		t.Fatalf("frame: %+v", f)
	}
	want := []Seat{
		{Name: "Ava", Side: "social", Pos: [3]float64{-12, 1.6, 3.1}},
		{Name: "Cy", Side: "social", Pos: [3]float64{40, 2, -20}},
		{Name: "slot 7", Side: "social", Pos: [3]float64{1, 1, 1}},
	}
	if len(f.Seats) != len(want) {
		t.Fatalf("seats: %+v", f.Seats)
	}
	for i := range want {
		if f.Seats[i] != want[i] {
			t.Errorf("seat %d: %+v, want %+v", i, f.Seats[i], want[i])
		}
	}

	// A player who walks into the lobby's arena joins blue.
	sw := []*tv2.EchoEvent{{Event: &tv2.EchoEvent_PlayerSwitchedTeam{PlayerSwitchedTeam: &tv2.PlayerSwitchedTeam{
		PlayerSlot: 3, PrevRole: tv2.Role_ROLE_SOCIAL_PARTICIPANT, NewRole: tv2.Role_ROLE_BLUE_TEAM}}}}
	feed(t, &d, arenaFrame(sw, &tv2.PlayerState{Slot: 3, Head: head(-60, 1, -30)}))
	if f := l.Get(); len(f.Seats) != 1 || f.Seats[0].Name != "Bo" || f.Seats[0].Side != "blue" {
		t.Errorf("after the switch: %+v", f.Seats)
	}

	// A new capture forgets the old one's frame.
	feed(t, &d, lobbyHeader("NEXT"))
	if f := l.Get(); f.Session != "NEXT" || !f.At.IsZero() || len(f.Seats) != 0 {
		t.Errorf("after a new header: %+v", f)
	}
}

func TestLatestLeavesArenaSidesAlone(t *testing.T) {
	l := &Latest{}
	d := Decoder{Latest: l}
	hdr := &tv2.Envelope{Message: &tv2.Envelope_Header{Header: &tv2.CaptureHeader{GameType: "echo_arena",
		GameHeader: &tv2.CaptureHeader_EchoArena{EchoArena: &tv2.EchoArenaHeader{SessionId: "S"}}}}}
	feed(t, &d, hdr, arenaFrame(nil, &tv2.PlayerState{Slot: 2, Head: head(1, 2, 3)}))
	if f := l.Get(); len(f.Seats) != 1 || f.Seats[0].Side != "" {
		t.Errorf("an unnamed arena player got a side: %+v", f.Seats)
	}
}

// TestDumpLatest replays a dump through Latest and counts what the live map
// would get (no names): TELEMETRY_DUMP=<file.bin> go test ./pkg/telemetry -run DumpLatest -v
func TestDumpLatest(t *testing.T) {
	path := os.Getenv("TELEMETRY_DUMP")
	if path == "" {
		t.Skip("set TELEMETRY_DUMP")
	}
	b, _ := os.ReadFile(path)
	l := &Latest{}
	d := Decoder{Latest: l}
	frames, withSeats, seats := 0, 0, 0
	sides := map[string]int{}
	for len(b) >= 4 {
		n := binary.LittleEndian.Uint32(b)
		if len(b) < 4+int(n) {
			break
		}
		_ = d.Envelope(b[4 : 4+n])
		b = b[4+n:]
		if d.Counts.Frames > frames {
			frames = d.Counts.Frames
			f := l.Get()
			if len(f.Seats) > 0 {
				withSeats++
			}
			for _, s := range f.Seats {
				seats++
				sides[s.Side]++
				if strings.HasPrefix(s.Name, "slot ") {
					sides["unnamed"]++
				}
			}
		}
	}
	f := l.Get()
	t.Logf("game %s map %s: frames %d, with positions %d, seats %d, sides %v", f.GameType, f.Map, frames, withSeats, seats, sides)
}

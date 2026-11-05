package telemetry

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

func TestDecoderNamesSlots(t *testing.T) {
	hdr := &tv2.Envelope{Message: &tv2.Envelope_Header{Header: &tv2.CaptureHeader{GameType: "echo_arena",
		GameHeader: &tv2.CaptureHeader_EchoArena{EchoArena: &tv2.EchoArenaHeader{SessionId: "S", InitialRoster: []*tv2.PlayerInfo{
			{Slot: 1, DisplayName: "Ava", Role: tv2.Role_ROLE_BLUE_TEAM},
			{Slot: 2, DisplayName: "N", Role: tv2.Role_ROLE_ORANGE_TEAM}}}}}}}
	victim := int32(2)
	frame := &tv2.Envelope{Message: &tv2.Envelope_Frame{Frame: &tv2.Frame{Payload: &tv2.Frame_EchoArena{EchoArena: &tv2.EchoArenaFrame{Events: []*tv2.EchoEvent{
		{Event: &tv2.EchoEvent_PlayerStun{PlayerStun: &tv2.PlayerStun{PlayerSlot: 1, TotalStuns: 3}}},
		{Event: &tv2.EchoEvent_PlayerSteal{PlayerSteal: &tv2.PlayerSteal{PlayerSlot: 1, TotalSteals: 1, VictimPlayerSlot: &victim}}},
		{Event: &tv2.EchoEvent_PlayerSave{PlayerSave: &tv2.PlayerSave{PlayerSlot: 9, TotalSaves: 1}}},
	}}}}}}
	var plays []Play
	d := Decoder{OnPlay: func(p Play) { plays = append(plays, p) }}
	for _, m := range []proto.Message{hdr, frame} {
		b, _ := proto.Marshal(m)
		if err := d.Envelope(b); err != nil {
			t.Fatal(err)
		}
	}
	if len(plays) != 3 || plays[0].Text() != "Ava stuns (3)" || plays[0].Team != "blue" {
		t.Fatalf("plays: %+v", plays)
	}
	if plays[1].Text() != "Ava steals the disc from N" || plays[2].Player != "slot 9" {
		t.Errorf("steal/unknown slot: %+v", plays[1:])
	}
	if d.Counts.Session != "S" || d.Counts.Frames != 1 {
		t.Errorf("counts: %+v", d.Counts)
	}
}

// TestDumpSummary prints what a dump from a real server holds:
// TELEMETRY_DUMP=<file.bin> go test ./pkg/telemetry -run DumpSummary -v
func TestDumpSummary(t *testing.T) {
	path := os.Getenv("TELEMETRY_DUMP")
	if path == "" {
		t.Skip("set TELEMETRY_DUMP")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d Decoder
	var plays []Play
	d.OnPlay = func(p Play) { plays = append(plays, p) }
	for len(b) >= 4 {
		n := binary.LittleEndian.Uint32(b)
		if len(b) < 4+int(n) {
			break
		}
		if err := d.Envelope(b[4 : 4+n]); err != nil {
			t.Logf("bad record: %v", err)
		}
		b = b[4+n:]
	}
	c := d.Counts
	t.Logf("game %q session %q: %d headers, %d frames (avg %d bytes), %d footers, %d events, %d bad",
		c.GameType, c.Session, c.Headers, c.Frames, c.FrameBytes/max(c.Frames, 1), c.Footers, c.Events, c.Bad)
	keys := make([]string, 0, len(c.ByEvent))
	for k := range c.ByEvent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-40s %d", k, c.ByEvent[k])
	}
	for i, p := range plays {
		if i < 15 {
			t.Logf("  play: %s", p.Text())
		}
	}
}

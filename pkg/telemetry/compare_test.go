package telemetry

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"testing"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

// TestDumpCompare matches a dump's stat events against a match.json (players
// by number, no names): TELEMETRY_DUMP=<bin> MATCH_JSON=<json> go test -run DumpCompare -v
func TestDumpCompare(t *testing.T) {
	path, mj := os.Getenv("TELEMETRY_DUMP"), os.Getenv("MATCH_JSON")
	if path == "" || mj == "" {
		t.Skip("set TELEMETRY_DUMP and MATCH_JSON")
	}
	var m struct {
		Blue, Orange struct {
			Players []struct {
				Name  string
				Stats map[string]float64
			}
		}
	}
	b, _ := os.ReadFile(mj)
	_ = json.Unmarshal(b, &m)
	idx := map[string]int{}
	api := map[int]map[string]float64{}
	for _, p := range append(m.Blue.Players, m.Orange.Players...) {
		idx[p.Name] = len(idx) + 1
		api[idx[p.Name]] = p.Stats
	}
	got := map[int]map[string]int{}
	slots := map[int]string{}
	var d Decoder
	d.OnPlay = func(p Play) {
		n := idx[p.Player]
		if got[n] == nil {
			got[n] = map[string]int{}
		}
		got[n][p.Kind] = p.Total
	}
	raw, _ := os.ReadFile(path)
	last := &tv2.EchoArenaFrame{}
	for len(raw) >= 4 {
		n := binary.LittleEndian.Uint32(raw)
		if len(raw) < 4+int(n) {
			break
		}
		_ = d.Envelope(raw[4 : 4+n])
		var env tv2.Envelope
		if proto.Unmarshal(raw[4:4+n], &env) == nil && env.GetFrame().GetEchoArena() != nil {
			last = env.GetFrame().GetEchoArena()
		}
		raw = raw[4+n:]
	}
	for s, st := range d.roster {
		slots[idx[st.name]] = "slot " + string(rune('0'+s%10))
	}
	for n := 1; n <= len(idx); n++ {
		t.Logf("player %d (%s): api stuns=%v saves=%v passes=%v steals=%v | telemetry %v", n, slots[n], api[n]["stuns"], api[n]["saves"], api[n]["passes"], api[n]["steals"], got[n])
	}
	for _, p := range last.GetPlayers() {
		t.Logf("last frame: slot %d flags=%#x", p.GetSlot(), p.GetFlags())
	}
}

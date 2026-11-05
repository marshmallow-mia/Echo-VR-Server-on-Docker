package telemetry

import (
	"encoding/binary"
	"os"
	"testing"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

// TestDumpPoses: do frames carry player positions? TELEMETRY_DUMP=<bin> go test -run DumpPoses -v
func TestDumpPoses(t *testing.T) {
	path := os.Getenv("TELEMETRY_DUMP")
	if path == "" {
		t.Skip("set TELEMETRY_DUMP")
	}
	b, _ := os.ReadFile(path)
	frames, withPlayers, nonzero := 0, 0, 0
	var sample []string
	for len(b) >= 4 {
		n := binary.LittleEndian.Uint32(b)
		if len(b) < 4+int(n) {
			break
		}
		var env tv2.Envelope
		_ = proto.Unmarshal(b[4:4+n], &env)
		b = b[4+n:]
		a := env.GetFrame().GetEchoArena()
		if a == nil {
			continue
		}
		frames++
		if len(a.GetPlayers()) > 0 {
			withPlayers++
		}
		for _, p := range a.GetPlayers() {
			h := p.GetHead().GetPosition()
			if h != nil && (h.GetX() != 0 || h.GetY() != 0 || h.GetZ() != 0) {
				nonzero++
				if len(sample) < 4 {
					sample = append(sample, h.String())
				}
			}
		}
		if frames == 2000 && len(a.GetPlayers()) > 0 {
			t.Logf("frame 2000 player 0: %v", a.GetPlayers()[0])
		}
	}
	t.Logf("%d frames, %d with players, %d non-zero head positions, samples %v", frames, withPlayers, nonzero, sample)
}

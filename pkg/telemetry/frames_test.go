package telemetry

import (
	"encoding/binary"
	"os"
	"testing"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

// TestDumpFrames samples frames of a dump: TELEMETRY_DUMP=<file> go test -run DumpFrames -v
func TestDumpFrames(t *testing.T) {
	path := os.Getenv("TELEMETRY_DUMP")
	if path == "" {
		t.Skip("set TELEMETRY_DUMP")
	}
	b, _ := os.ReadFile(path)
	i := 0
	for len(b) >= 4 {
		n := binary.LittleEndian.Uint32(b)
		if len(b) < 4+int(n) {
			break
		}
		var env tv2.Envelope
		_ = proto.Unmarshal(b[4:4+n], &env)
		b = b[4+n:]
		if f := env.GetFrame(); f != nil {
			a := f.GetEchoArena()
			if i%250 == 0 || len(a.GetEvents()) > 0 {
				t.Logf("#%d idx=%d ts=%dms key=%v status=%v clock=%.1f pts=%d-%d round=%d players=%d bones=%d disc=%v events=%d size=%d",
					i, f.GetFrameIndex(), f.GetTimestampOffsetMs(), f.GetIsKeyframe(), a.GetGameStatus(), a.GetGameClock(), a.GetBluePoints(), a.GetOrangePoints(),
					a.GetRoundNumber(), len(a.GetPlayers()), len(a.GetPlayerBones()), a.GetDisc().GetPose().GetPosition() != nil, len(a.GetEvents()), n)
			}
			i++
		}
	}
}

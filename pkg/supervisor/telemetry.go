package supervisor

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/telemetry"
)

// withTelemetry adds a telemetry section that points the runtime at the
// local ingest. A config that has its own telemetry section is the
// operator's choice and stays as it is (ok false).
func withTelemetry(yaml []byte, uri, token string) ([]byte, bool) {
	c, err := config.Parse(yaml)
	if err != nil || c.Telemetry != nil {
		return yaml, false
	}
	out := append([]byte(nil), yaml...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	// The token is ours: without one the runtime would send the server's own
	// sign-in token.
	out = append(out, fmt.Sprintf("telemetry:\n  uri: %q\n  token: %q\n", uri, token)...)
	return out, true
}

// serveTelemetry runs the local ingest for the supervisor's lifetime.
func (s *Supervisor) serveTelemetry(ctx context.Context) {
	in := s.tele
	if s.o.TelemetryDump {
		in.DumpDir = filepath.Join(s.o.Home, "telemetry-dump")
	}
	var dec telemetry.Decoder
	if s.o.TelemetryPlays {
		dec.OnPlay = func(p telemetry.Play) {
			s.note(match.Note{Kind: match.NotePlay, Player: p.Player, Team: p.Team, Play: p.Kind, Total: p.Total, Text: p.Text()})
		}
	}
	in.OnEnvelope = func(b []byte) { _ = dec.Envelope(b) }
	in.OnConnect = func() {
		s.update(func(st *status.Status) { st.TelemetryConnected = true })
		s.logf("telemetry: the game connected")
	}
	in.OnClose = func(err error, envs, bytes int64, d time.Duration) {
		s.update(func(st *status.Status) { st.TelemetryConnected = false })
		s.logf("telemetry: connection closed after %s: %d records, %d KB (%v)", d.Round(time.Second), envs, bytes>>10, err)
	}
	if err := in.Serve(ctx); err != nil {
		s.logf("telemetry: cannot listen on %s: %v", in.Addr, err)
	}
}

// setTelemetry stores the switch; like the API switch, an idle game restarts
// to pick it up, one in a match picks it up at its next start.
func (s *Supervisor) setTelemetry(on bool) {
	want := status.OnOff(on)
	if err := match.SetTelemetry(s.o.Home, on); err != nil {
		s.logf("telemetry: cannot save the setting: %v", err)
		return
	}
	st := s.Status()
	switch {
	case st.PID == 0:
		s.logf("telemetry %s from the next start", want)
	case st.Telemetry == want:
		s.logf("telemetry is already %s", want)
	case st.State == status.StateRegistered:
		s.logf("telemetry %s: restarting the idle game to apply it", want)
		s.Restart()
	default:
		s.logf("telemetry %s after this match (the game reads its config when it starts)", want)
	}
}

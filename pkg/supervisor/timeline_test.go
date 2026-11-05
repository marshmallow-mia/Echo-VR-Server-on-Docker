package supervisor

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
)

// replay feeds a log through Parse into a tracker, as the supervisor does, with
// the session API off. It returns the summaries of the finished sessions and
// the last match.
func replay(t *testing.T, path string) ([]match.Summary, match.Match) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var (
		tr   match.Tracker
		sums []match.Summary
		last match.Match
	)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		at, err := time.Parse(time.RFC3339Nano, strings.Fields(line)[0])
		if err != nil {
			continue
		}
		ev := Parse(line)
		switch ev.Kind {
		case KindSessionStart:
			tr.Start(ev.Session, "", "", at)
			tr.SetAPI(match.APIOff, "")
		case KindLevel:
			tr.SetLog(ev.GameType, ev.Level)
		case KindSessionEnd:
			last = tr.Match()
			if s, ok := tr.Finish(at); ok {
				sums = append(sums, s)
			}
		default:
			if n, ok := ev.Note(); ok && tr.Active() {
				n.At = at
				tr.Note(n)
			}
		}
	}
	if tr.Active() {
		last = tr.Match()
	}
	return sums, last
}

func TestLogTimelineArenaLobbyCombat(t *testing.T) {
	sums, live := replay(t, "testdata/sessions-2026-11-04.log")
	if len(sums) != 3 {
		t.Fatalf("want arena, lobby, combat finished; got %d", len(sums))
	}
	arena, lobby, combat := sums[0], sums[1], sums[2]
	if arena.GameType != "echo_arena" || len(arena.Players) < 8 {
		t.Errorf("arena: %s with %d players", arena.GameType, len(arena.Players))
	}
	for _, p := range arena.Players {
		if p.Name == match.HiddenName || p.Name == "Player01" {
			t.Errorf("a hidden player is in the summary: %+v", p)
		}
	}
	if !match.IsLobby(lobby.GameType) {
		t.Errorf("second session: %s", lobby.GameType)
	}
	if combat.GameType != "echo_combat" {
		t.Errorf("third session: %s", combat.GameType)
	}
	// The running combat match (Fission) is live: players from the log, round count.
	if !live.Active || live.Map != "mpl_combat_fission" || live.Players == 0 || live.Blue == nil {
		t.Fatalf("live: active=%v map=%s players=%d", live.Active, live.Map, live.Players)
	}
}

func TestLogTimelineEvents(t *testing.T) {
	f, _ := os.ReadFile("testdata/sessions-2026-11-04.log")
	arenaLog := strings.SplitAfter(string(f), "Dedicated: session ended (no participants)\n")[0]
	tmp := t.TempDir() + "/arena.log"
	if err := os.WriteFile(tmp, []byte(arenaLog), 0o644); err != nil {
		t.Fatal(err)
	}
	sums, m := replay(t, tmp)
	if len(sums) != 1 {
		t.Fatalf("summaries: %d", len(sums))
	}
	if m.Round != 1 || m.Phase != match.PhaseReturning || m.RoundStartedAt.IsZero() {
		t.Errorf("phase: round=%d phase=%s round_started=%v", m.Round, m.Phase, m.RoundStartedAt)
	}
	text := map[string]bool{}
	var lost string
	for _, e := range m.Events {
		text[e.Text] = true
		if strings.Contains(e.Text, "lost the connection") {
			lost = e.Text
		}
	}
	for _, want := range []string{"Waiting for players", "Round 1 starting", "Match over", "Player04 rejoined Blue"} {
		if !text[want] {
			t.Errorf("missing event %q in %v", want, text)
		}
	}
	if lost != "Player04 lost the connection (ping 50 ms, max 58 ms)" {
		t.Errorf("leave: %q", lost)
	}
	goals := 0
	for _, e := range m.Events {
		if e.Text == "Point scored" {
			goals++
		}
	}
	if goals != 2 {
		t.Errorf("points from the log: %d, want 2", goals)
	}
}

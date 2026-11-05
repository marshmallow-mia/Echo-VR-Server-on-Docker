package match

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestLiveFrom(t *testing.T) {
	s, err := ParseSession(200, []byte(`{"sessionid":"ABC-D","match_type":"Echo_Arena","map_name":"mpl_arena_a",
		"disc":{"position":[0.04,1.06,-3]},
		"teams":[{"team":"ORANGE TEAM","players":[{"name":"O","head":{"position":[1,2,3]}}]},
		{"team":"BLUE TEAM","players":[{"name":"B","stunned":true,"head":{"position":[4,5,6]}},{"name":"NoHead"}]},
		{"team":"SPECTATORS","players":[{"name":"S","head":{"position":[0,0,0]}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1700000000123)
	l := LiveFrom(s, now)
	if l.SessionID != "abc-d" || l.AtMS != 1700000000123 || l.Map != "mpl_arena_a" {
		t.Errorf("header: %+v", l)
	}
	if len(l.Disc) != 3 || l.Disc[0] != 0 || l.Disc[1] != 1.1 {
		t.Errorf("disc: %v", l.Disc)
	}
	if len(l.Players) != 2 || l.Players[0].Name != "B" || l.Players[0].Side != "blue" || !l.Players[0].Stunned ||
		l.Players[1].Name != "O" || l.Players[1].Side != "orange" {
		t.Errorf("players: %+v", l.Players)
	}

	home := t.TempDir()
	if err := WriteLive(home, l); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadLive(home); err != nil || len(got.Players) != 2 {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if err := RemoveLive(home); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLive(home); err != nil {
		t.Errorf("second remove: %v", err)
	}
	if _, err := ReadLive(home); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after remove: %v", err)
	}
}

func TestLiveFromCombatHasNoDisc(t *testing.T) {
	s, _ := ParseSession(200, []byte(`{"sessionid":"X","match_type":"Echo_Combat","disc":{"position":[1,2,3]},"teams":[]}`))
	if l := LiveFrom(s, time.Now()); l.Disc != nil || l.Players == nil {
		t.Errorf("combat: %+v", l)
	}
}

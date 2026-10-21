package game

import "testing"

func TestParseItemized(t *testing.T) {
	out := ".d..t...... ./\n>f.st...... bin/win10/echovr.exe\n*deleting   old.dll\ncd+++++++++ newdir/\n>f+++++++++ newdir/a.pak\n.f...p..... same.txt\n"
	got := parseItemized(out)
	want := []string{"bin/win10/echovr.exe", "old.dll", "newdir/a.pak"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q want %q", i, got[i], want[i])
		}
	}
}

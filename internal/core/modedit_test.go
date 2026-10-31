package core

import (
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
)

func TestModEdits(t *testing.T) {
	dir := t.TempDir()
	cfg := hostcfg.New()
	cfg.DataDir = t.TempDir()
	cfg.Instances = []hostcfg.Instance{{Name: "echo-1", Port: 6792}, {Name: "echo-2", Port: 6793}}
	a := &App{Dir: dir, Cfg: cfg, Secrets: hostcfg.Secrets{}}
	if err := a.AddMod("", mods.Spec{ID: "server-patches", Args: map[string]string{"teamsize": "4"}}); err != nil {
		t.Fatal(err)
	}
	// One server leaves it out, the other sets its own argument.
	if err := a.RemoveMod("echo-1", "server-patches"); err != nil {
		t.Fatal(err)
	}
	if err := a.AddMod("echo-2", mods.Spec{ID: "server-patches", Args: map[string]string{"teamsize": "6"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.PinMod("echo-2", "server-patches", "0.4.1"); err != nil {
		t.Fatal(err)
	}
	got := map[string]mods.ServerMods{}
	for _, s := range a.ModServers() {
		got[s.Name] = s
	}
	one := got["echo-1"].Specs
	if len(one) != 1 || one[0].Enabled == nil || *one[0].Enabled {
		t.Errorf("echo-1: %+v", one)
	}
	two := got["echo-2"].Specs
	if len(two) != 1 || two[0].Args["teamsize"] != "6" || two[0].Version != "0.4.1" {
		t.Errorf("echo-2: %+v", two)
	}
	// It is saved, and reads back the same.
	re, err := hostcfg.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if in, _ := re.Instance("echo-2"); len(in.Settings.Mods) != 1 || re.Defaults.Mods[0].Args["teamsize"] != "4" {
		t.Errorf("saved: %+v %+v", in.Settings.Mods, re.Defaults.Mods)
	}
	// Taking it off the defaults leaves echo-2's own entry.
	if err := a.RemoveMod("", "server-patches"); err != nil {
		t.Fatal(err)
	}
	if len(a.Cfg.Defaults.Mods) != 0 {
		t.Errorf("defaults: %+v", a.Cfg.Defaults.Mods)
	}
	if err := a.AddMod("", mods.Spec{ID: "x", Args: map[string]string{"k": "${HOME}"}}); err == nil {
		t.Error("an argument with ${...} was saved")
	}
	if len(a.Cfg.Defaults.Mods) != 0 {
		t.Errorf("a refused edit stayed: %+v", a.Cfg.Defaults.Mods)
	}
	if err := a.SetDevCodes([]string{"not-a-code"}); err == nil {
		t.Error("a bad dev code was saved")
	}
}

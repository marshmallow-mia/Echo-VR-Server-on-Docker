package core

import (
	"slices"
	"testing"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
)

// Only a server that runs current gets the runtime policy: a pinned one runs
// its release with the runtime's own update off whatever the policy says, so
// changing the policy must not recreate it.
func TestSpecPolicyOnlyForCurrent(t *testing.T) {
	cfg := hostcfg.New()
	cfg.DataDir = t.TempDir()
	cfg.Runtime.Updater, cfg.Runtime.Channel = hostcfg.UpdaterNevr, "beta"
	a := &App{Dir: t.TempDir(), Cfg: cfg, Secrets: hostcfg.Secrets{}}
	cur := hostcfg.Instance{Name: "echo-1", Port: 6792}
	pinned := hostcfg.Instance{Name: "echo-2", Port: 6793, Settings: instance.Settings{Runtime: "v6.16.5"}}
	if env := a.spec(cur).Env; !slices.Contains(env, "EVR_RUNTIME_UPDATER=nevr") || !slices.Contains(env, "NEVR_UPDATE_CHANNEL=beta") {
		t.Errorf("current: %q", env)
	}
	for _, e := range a.spec(pinned).Env {
		if e == "EVR_RUNTIME_UPDATER=nevr" || e == "NEVR_UPDATE_CHANNEL=beta" {
			t.Errorf("pinned got %s", e)
		}
	}
}

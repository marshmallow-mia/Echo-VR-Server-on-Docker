package supervisor

import (
	"fmt"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

// selfUpdate is how one start handles the runtime's own update (v6.17.0 on).
type selfUpdate struct {
	applies bool   // the runtime updates itself (two-file, nevr.UpdateMin on)
	config  bool   // its config.yaml takes the update section (nevr.UpdateConfigMin on)
	nevr    bool   // the runtime keeps itself current; else the host tool does
	channel string // the channel it follows (nevr only)
}

// selfUpdateFor decides for a start of the store's release tag. A server
// pinned to a release (Runtime is a tag, which includes the update canary)
// runs that release: its own update is off whatever the updater is. So is
// that of a 6.17.x runtime: its channel can't be chosen (nevr.UpdateConfigMin).
func (s *Supervisor) selfUpdateFor(twoFile bool, tag string) selfUpdate {
	u := selfUpdate{applies: twoFile && nevr.SelfUpdates(tag)}
	if !u.applies {
		return u
	}
	u.config = nevr.UpdateConfigurable(tag)
	u.nevr = u.config && s.o.Updater == nevr.UpdaterNevr && s.o.Runtime == "current"
	if u.nevr {
		u.channel = nevr.ChannelName(s.o.UpdateChannel)
	}
	return u
}

// args is the game's command line addition.
func (u selfUpdate) args() []string {
	if u.applies && !u.nevr {
		return []string{"-no-update"}
	}
	return nil
}

// withUpdate adds the update section to a root config.yaml. A config that has
// its own update section is the operator's choice and stays as it is
// (ok false), as does one for a runtime that doesn't take the section.
func withUpdate(yaml []byte, u selfUpdate) ([]byte, bool) {
	if !u.config {
		return yaml, false
	}
	c, err := config.Parse(yaml)
	if err != nil || c.Update != nil {
		return yaml, false
	}
	out := append([]byte(nil), yaml...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if u.nevr {
		out = append(out, fmt.Sprintf("update:\n  enabled: true\n  channel: %q\n", u.channel)...)
	} else {
		out = append(out, "update:\n  enabled: false\n"...)
	}
	return out, true
}

// keepRuntime reports whether the runtime nEVR's own update put in root stays:
// with the runtime keeping itself current, one not older than the store's
// release tag is never replaced by it.
func keepRuntime(u selfUpdate, root, tag string) (string, bool) {
	if !u.nevr {
		return "", false
	}
	have, ok := game.SelfUpdated(root)
	if !ok {
		return "", false
	}
	hv, err1 := nevr.ParseVersion(have)
	sv, err2 := nevr.ParseVersion(tag)
	if err1 != nil || err2 != nil || hv.Less(sv) {
		return have, false
	}
	return have, true
}

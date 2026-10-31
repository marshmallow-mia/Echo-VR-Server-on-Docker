package core

import (
	"fmt"
	"slices"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/mods"
)

// settingsOf returns the settings a mod edit changes: the defaults (server "")
// or one server's own.
func (a *App) settingsOf(server string) (*instance.Settings, error) {
	if server == "" {
		return &a.Cfg.Defaults, nil
	}
	in, ok := a.Cfg.Instance(server)
	if !ok {
		return nil, fmt.Errorf("no server named %q", server)
	}
	return &in.Settings, nil
}

// editMods runs edit on the settings of server ("" = the defaults) and saves;
// a refused edit leaves them as they were.
func (a *App) editMods(server string, edit func(set *instance.Settings) error) error {
	set, err := a.settingsOf(server)
	if err != nil {
		return err
	}
	mods0 := slices.Clone(set.Mods)
	local0 := set.LocalPlugins
	err = edit(set)
	if err == nil {
		err = a.Cfg.Validate()
	}
	if err == nil {
		err = a.Save()
	}
	if err != nil {
		set.Mods, set.LocalPlugins = mods0, local0
	}
	return err
}

// AddMod adds a mod to the defaults (server "") or one server, or changes its
// version, prerelease choice and arguments when it is listed already.
func (a *App) AddMod(server string, s mods.Spec) error {
	return a.editMods(server, func(set *instance.Settings) error {
		i := slices.IndexFunc(set.Mods, func(m mods.Spec) bool { return m.ID == s.ID })
		if i < 0 {
			set.Mods = append(set.Mods, s)
			return nil
		}
		m := &set.Mods[i]
		m.Enabled = nil
		if s.Version != "" {
			m.Version = s.Version
		}
		m.Prerelease = m.Prerelease || s.Prerelease
		if len(s.Args) > 0 {
			args := map[string]string{}
			for k, v := range m.Args {
				args[k] = v
			}
			for k, v := range s.Args {
				args[k] = v
			}
			m.Args = args
		}
		return nil
	})
}

// RemoveMod takes a mod off the defaults, or off one server: a mod of the
// defaults stays listed there with enabled: false.
func (a *App) RemoveMod(server, id string) error {
	inDefaults := slices.ContainsFunc(a.Cfg.Defaults.Mods, func(m mods.Spec) bool { return m.ID == id })
	return a.editMods(server, func(set *instance.Settings) error {
		i := slices.IndexFunc(set.Mods, func(m mods.Spec) bool { return m.ID == id })
		switch {
		case server != "" && inDefaults:
			off := false
			if i < 0 {
				set.Mods = append(set.Mods, mods.Spec{ID: id, Enabled: &off})
			} else {
				set.Mods[i] = mods.Spec{ID: id, Enabled: &off}
			}
		case i >= 0:
			set.Mods = slices.Delete(set.Mods, i, i+1)
		default:
			return fmt.Errorf("%s isn't in the list", id)
		}
		return nil
	})
}

// PinMod pins a mod to a version or range ("" follows the newest again).
func (a *App) PinMod(server, id, version string) error {
	inDefaults := slices.ContainsFunc(a.Cfg.Defaults.Mods, func(m mods.Spec) bool { return m.ID == id })
	return a.editMods(server, func(set *instance.Settings) error {
		if i := slices.IndexFunc(set.Mods, func(m mods.Spec) bool { return m.ID == id }); i >= 0 {
			set.Mods[i].Version = version
			return nil
		}
		if server == "" || !inDefaults {
			return fmt.Errorf("%s isn't in the list: add it first", id)
		}
		set.Mods = append(set.Mods, mods.Spec{ID: id, Version: version})
		return nil
	})
}

// SetLocalPlugins turns a server's own DLLs (instances/<name>/mods/local) on or
// off; on the defaults it is every server's default.
func (a *App) SetLocalPlugins(server string, on bool) error {
	return a.editMods(server, func(set *instance.Settings) error {
		set.LocalPlugins = &on
		return nil
	})
}

// SetDevCodes stores the mod dev codes in secrets.env.
func (a *App) SetDevCodes(codes []string) error {
	for _, c := range codes {
		if mods.NormalizeCode(c) == "" {
			return fmt.Errorf("%q isn't a dev code", c)
		}
	}
	a.Secrets.SetDevCodes(codes)
	return a.Save()
}

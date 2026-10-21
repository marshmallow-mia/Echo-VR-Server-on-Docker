// Package core is evr-docker's service layer. The CLI and the TUI only call
// into it, so every TUI action has a command-line equivalent.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/docker"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/instance"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/auth"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// Container labels.
const (
	LabelManaged  = "evr.managed-by"
	LabelProject  = "evr.project"
	LabelInstance = "evr.instance"
	LabelSpec     = "evr.spec"
	ManagedBy     = "evr-docker"
	// StopTimeout gives the supervisor time for Ctrl+C plus its own hard kill.
	StopTimeout = 40 * time.Second
)

// App bundles configuration and clients.
type App struct {
	Dir     string
	Cfg     *hostcfg.Config
	Secrets hostcfg.Secrets
	docker  *docker.Client
}

// Open loads the configuration in dir.
func Open(dir string) (*App, error) {
	cfg, err := hostcfg.Load(dir)
	if err != nil {
		return nil, err
	}
	sec, err := hostcfg.LoadSecrets(dir)
	if err != nil {
		return nil, err
	}
	return &App{Dir: dir, Cfg: cfg, Secrets: sec}, nil
}

// Docker returns the Docker client, connecting on first use.
func (a *App) Docker() (*docker.Client, error) {
	if a.docker == nil {
		c, err := docker.New()
		if err != nil {
			return nil, err
		}
		a.docker = c
	}
	return a.docker, nil
}

// Save writes config and secrets.
func (a *App) Save() error {
	if err := a.Cfg.Save(a.Dir); err != nil {
		return err
	}
	return a.Secrets.Save(a.Dir)
}

// Store is the nevr-runtime store.
func (a *App) Store() nevr.Store { return nevr.Store{Dir: a.Cfg.NevrDir()} }

// Games is the game installer.
func (a *App) Games(progress func(game.Progress)) game.Installer {
	return game.Installer{Dir: a.Cfg.GameDir(), Progress: progress}
}

// Auth is a backend client for the configured account.
func (a *App) Auth() *auth.Client {
	uri := a.Cfg.Defaults.HTTPURI
	if uri == "" {
		uri = config.DefaultHTTPURI
	}
	return &auth.Client{HTTPURI: uri, HTTPKey: a.Secrets[config.EnvHTTPKey]}
}

// Login checks the configured account.
func (a *App) Login(ctx context.Context) (auth.Session, error) {
	if a.Secrets[config.EnvPassword] == "" && a.DeviceLinked() {
		return a.refreshDevice(ctx)
	}
	if m := a.Secrets.Missing(a.DeviceLinked()); len(m) > 0 {
		return auth.Session{}, fmt.Errorf("%w (%v missing)", ErrNoAccount, m)
	}
	return a.Auth().Login(ctx, a.Secrets[config.EnvDiscordID], a.Secrets[config.EnvPassword])
}

// Server is the combined view of one configured instance.
type Server struct {
	Instance  hostcfg.Instance
	Settings  instance.Settings
	Container *docker.Container // nil when not created
	Status    *status.Status    // nil before the first start
	// Drift is set when the container does not match the configuration and
	// needs `server apply`.
	Drift string
}

// Running reports whether the container is up.
func (s Server) Running() bool { return s.Container != nil && s.Container.State == "running" }

// State is a one-word summary for tables.
func (s Server) State() string {
	switch {
	case s.Instance.Disabled && !s.Running():
		return "disabled"
	case s.Container == nil:
		return "not created"
	case !s.Running():
		return s.Container.State
	case s.Status == nil || s.Status.Stale(time.Now(), 15*time.Second) || s.Status.UpdatedAt.Before(s.Container.Created):
		// no fresh status from this container yet
		return "starting"
	default:
		return string(s.Status.State)
	}
}

// Servers lists configured instances with their container and status.
func (a *App) Servers(ctx context.Context) ([]Server, error) {
	d, err := a.Docker()
	if err != nil {
		return nil, err
	}
	// Only this config's containers: several configs can share one Docker host.
	conts, err := d.List(ctx, LabelProject+"="+a.Cfg.Project)
	if err != nil {
		return nil, err
	}
	imageID, _ := d.ImageID(ctx, a.Cfg.Image)
	byName := map[string]*docker.Container{}
	for i := range conts {
		byName[conts[i].Labels[LabelInstance]] = &conts[i]
	}
	var out []Server
	for _, in := range a.Cfg.Instances {
		s := Server{Instance: in, Settings: a.Cfg.Effective(in), Container: byName[in.Name]}
		if st, err := status.Read(filepath.Join(a.Cfg.InstanceDir(in.Name), status.FileName)); err == nil {
			s.Status = &st
		}
		if s.Container != nil {
			if want := a.specHash(in); s.Container.Labels[LabelSpec] != want {
				s.Drift = "settings changed since the container was created"
			} else if imageID != "" && s.Container.ImageID != "" && s.Container.ImageID != imageID {
				s.Drift = "a newer image is available"
			}
		}
		delete(byName, in.Name)
		out = append(out, s)
	}
	// Containers whose instance was removed from the config.
	for name, c := range byName {
		out = append(out, Server{Instance: hostcfg.Instance{Name: name}, Container: c, Drift: "not in the configuration any more"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Instance.Port < out[j].Instance.Port })
	return out, nil
}

// Server returns one server by name.
func (a *App) Server(ctx context.Context, name string) (Server, error) {
	all, err := a.Servers(ctx)
	if err != nil {
		return Server{}, err
	}
	for _, s := range all {
		if s.Instance.Name == name {
			return s, nil
		}
	}
	return Server{}, fmt.Errorf("no server named %q", name)
}

func (a *App) containerName(instance string) string { return a.Cfg.Project + "-" + instance }

// spec builds the container for an instance.
func (a *App) spec(in hostcfg.Instance) docker.ContainerSpec {
	set := a.Cfg.Effective(in)
	env := append([]string{
		"EVR_INSTANCE=" + in.Name,
		"EVR_PORT=" + strconv.Itoa(in.Port),
		"EVR_GAME_DIR=/opt/echovr/game/" + game.CurrentLink,
		"EVR_NEVR_DIR=/opt/echovr/nevr",
	}, set.Env()...)
	env = append(env, a.Secrets.GameEnv()...)
	res := a.Cfg.EffectiveResources(in)
	var ports []docker.Port
	for _, proto := range []string{"udp", "tcp"} {
		ports = append(ports, docker.Port{Port: in.Port, Proto: proto, HostIP: a.Cfg.BindIP})
	}
	return docker.ContainerSpec{
		Name:  a.containerName(in.Name),
		Image: a.Cfg.Image,
		User:  fmt.Sprintf("%d:%d", a.Cfg.UID, a.Cfg.UID),
		Env:   env,
		Labels: map[string]string{
			LabelManaged: ManagedBy, LabelProject: a.Cfg.Project, LabelInstance: in.Name,
		},
		Binds: []string{
			a.Cfg.InstanceDir(in.Name) + ":/home/container",
			a.Cfg.GameDir() + ":/opt/echovr/game:ro",
			a.Cfg.NevrDir() + ":/opt/echovr/nevr:ro",
		},
		Ports:       ports,
		NanoCPUs:    int64(res.CPUs * 1e9),
		Memory:      int64(res.MemoryMB) << 20,
		StopTimeout: int(StopTimeout.Seconds()),
	}
}

func (a *App) specHash(in hostcfg.Instance) string {
	s := a.spec(in)
	s.Labels = nil
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Apply makes containers match the configuration: missing ones are created,
// changed ones recreated (only when idle unless force), removed ones deleted.
// It returns what it did, one line per action.
func (a *App) Apply(ctx context.Context, force bool, progress func(string)) ([]string, error) {
	say := func(f string, args ...any) string {
		msg := fmt.Sprintf(f, args...)
		if progress != nil {
			progress(msg)
		}
		return msg
	}
	d, err := a.Docker()
	if err != nil {
		return nil, err
	}
	if len(a.Cfg.Instances) > 0 {
		if err := a.ensureImage(ctx, progress); err != nil {
			return nil, err
		}
	}
	if a.Secrets[config.EnvDiscordID] == "" && a.DeviceLinked() && len(a.Cfg.Instances) > 0 {
		if _, err := a.Login(ctx); err != nil { // fills in the Discord ID
			return nil, err
		}
	}
	if m := a.Secrets.Missing(a.DeviceLinked()); len(m) > 0 && len(a.Cfg.Instances) > 0 {
		return nil, fmt.Errorf("%w (%v missing)", ErrNoAccount, m)
	}
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var done []string
	var errs []error
	for _, s := range servers {
		name := s.Instance.Name
		_, configured := a.Cfg.Instance(name)
		switch {
		case !configured:
			if s.Status != nil && !s.Status.State.Idle() && !force {
				done = append(done, say("%s: removed from the config but hosting a match; left running (use --force)", name))
				continue
			}
			if err := d.Stop(ctx, s.Container.ID, StopTimeout); err != nil && !errors.Is(err, docker.ErrNotFound) {
				errs = append(errs, err)
			}
			if err := d.Remove(ctx, s.Container.ID, true); err != nil {
				errs = append(errs, err)
				continue
			}
			done = append(done, say("%s: container removed", name))
		case s.Container != nil && s.Drift != "":
			if s.Running() && s.Status != nil && !s.Status.State.Idle() && !force {
				done = append(done, say("%s: settings changed, will update after its match (run apply again, or --force)", name))
				continue
			}
			say("%s: recreating (%s)", name, s.Drift)
			if s.Running() {
				if err := d.Stop(ctx, s.Container.ID, StopTimeout); err != nil {
					errs = append(errs, err)
					continue
				}
			}
			if err := d.Remove(ctx, s.Container.ID, true); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := a.create(ctx, s.Instance); err != nil {
				errs = append(errs, err)
				continue
			}
			done = append(done, say("%s: recreated", name))
		case s.Container == nil && !s.Instance.Disabled:
			if err := a.create(ctx, s.Instance); err != nil {
				errs = append(errs, err)
				continue
			}
			done = append(done, say("%s: created and started on port %d", name, s.Instance.Port))
		case s.Container != nil && s.Instance.Disabled && s.Running():
			if err := d.Stop(ctx, s.Container.ID, StopTimeout); err != nil {
				errs = append(errs, err)
				continue
			}
			done = append(done, say("%s: stopped (disabled)", name))
		}
	}
	return done, errors.Join(errs...)
}

func (a *App) create(ctx context.Context, in hostcfg.Instance) error {
	if err := a.prepareInstanceDir(in.Name); err != nil {
		return err
	}
	d, err := a.Docker()
	if err != nil {
		return err
	}
	spec := a.spec(in)
	spec.Labels[LabelSpec] = a.specHash(in)
	id, err := d.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("%s: %w", in.Name, err)
	}
	if in.Disabled {
		return nil
	}
	return d.Start(ctx, id)
}

func (a *App) prepareInstanceDir(name string) error {
	dir := a.Cfg.InstanceDir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.Chown(dir, a.Cfg.UID, a.Cfg.UID); err != nil && os.Geteuid() == 0 {
		return err
	}
	return a.installDeviceLink(name, false)
}

func (a *App) ensureImage(ctx context.Context, progress func(string)) error {
	d, err := a.Docker()
	if err != nil {
		return err
	}
	if _, err := d.ImageID(ctx, a.Cfg.Image); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	if progress != nil {
		progress("pulling " + a.Cfg.Image)
	}
	return d.Pull(ctx, a.Cfg.Image, nil)
}

// Start, Stop and Restart act on one instance's container.
func (a *App) Start(ctx context.Context, name string) error {
	s, err := a.Server(ctx, name)
	if err != nil {
		return err
	}
	if s.Container == nil {
		_, err := a.Apply(ctx, false, nil)
		return err
	}
	d, _ := a.Docker()
	return d.Start(ctx, s.Container.ID)
}

func (a *App) Stop(ctx context.Context, name string) error {
	s, err := a.Server(ctx, name)
	if err != nil {
		return err
	}
	if s.Container == nil {
		return nil
	}
	d, _ := a.Docker()
	return d.Stop(ctx, s.Container.ID, StopTimeout)
}

// Restart restarts the container. With soft, only the game process restarts
// (via the supervisor), which is faster and keeps the container.
func (a *App) Restart(ctx context.Context, name string, soft bool) error {
	s, err := a.Server(ctx, name)
	if err != nil {
		return err
	}
	if s.Container == nil || !s.Running() {
		return a.Start(ctx, name)
	}
	if soft {
		p := filepath.Join(a.Cfg.InstanceDir(name), "control")
		if err := os.WriteFile(p, []byte("restart\n"), 0o644); err != nil {
			return err
		}
		return os.Chown(p, a.Cfg.UID, a.Cfg.UID)
	}
	d, _ := a.Docker()
	return d.Restart(ctx, s.Container.ID, StopTimeout)
}

// Logs streams an instance's console output.
func (a *App) Logs(ctx context.Context, name string, tail int, follow bool, line func(string)) error {
	d, err := a.Docker()
	if err != nil {
		return err
	}
	return d.Logs(ctx, a.containerName(name), tail, follow, line)
}

// AddInstances appends n instances on the next free ports.
func (a *App) AddInstances(n int, settings instance.Settings) ([]hostcfg.Instance, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	var added []hostcfg.Instance
	for i := 0; i < n; i++ {
		name, port := a.Cfg.NextFree()
		in := hostcfg.Instance{Name: name, Port: port, Settings: settings}
		a.Cfg.Instances = append(a.Cfg.Instances, in)
		added = append(added, in)
	}
	return added, a.Cfg.Validate()
}

// RemoveInstance drops an instance from the config; with purge its data
// directory (prefix, logs, credentials) is deleted too.
func (a *App) RemoveInstance(name string, purge bool) error {
	idx := -1
	for i, in := range a.Cfg.Instances {
		if in.Name == name {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("no server named %q", name)
	}
	a.Cfg.Instances = append(a.Cfg.Instances[:idx], a.Cfg.Instances[idx+1:]...)
	if purge {
		return os.RemoveAll(a.Cfg.InstanceDir(name))
	}
	return nil
}

// Scale keeps exactly n enabled instances, adding on free ports or removing
// the highest-numbered idle ones.
func (a *App) Scale(ctx context.Context, n int) ([]string, error) {
	if n < 0 {
		return nil, errors.New("count must be zero or more")
	}
	var msgs []string
	enabled := 0
	for _, in := range a.Cfg.Instances {
		if !in.Disabled {
			enabled++
		}
	}
	if n > enabled {
		added, err := a.AddInstances(n-enabled, instance.Settings{})
		if err != nil {
			return nil, err
		}
		for _, in := range added {
			msgs = append(msgs, fmt.Sprintf("added %s on port %d", in.Name, in.Port))
		}
		return msgs, nil
	}
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil, err
	}
	for i := len(servers) - 1; i >= 0 && enabled > n; i-- {
		s := servers[i]
		if _, ok := a.Cfg.Instance(s.Instance.Name); !ok || s.Instance.Disabled {
			continue
		}
		if s.Status != nil && !s.Status.State.Idle() {
			msgs = append(msgs, fmt.Sprintf("%s is hosting a match; kept", s.Instance.Name))
			continue
		}
		if err := a.RemoveInstance(s.Instance.Name, false); err != nil {
			return msgs, err
		}
		enabled--
		msgs = append(msgs, "removed "+s.Instance.Name)
	}
	return msgs, nil
}

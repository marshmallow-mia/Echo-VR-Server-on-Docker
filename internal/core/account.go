package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/game"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/auth"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

var (
	// ErrNoAccount means the servers have no way to sign in to EchoVRCE yet.
	ErrNoAccount = errors.New("the servers have no EchoVRCE sign-in yet")
	// ErrDeviceLink marks a device link that did not complete.
	ErrDeviceLink = errors.New("device link failed")
)

// DeviceFile holds the echovrce.com device link (refresh token), 0600.
const DeviceFile = "device.json"

// ResetPasswordNote is shown before a device link starts.
const ResetPasswordNote = "Device sign-in needs an EchoVRCE account without a password. If you ever set one for hosting, run /reset-password in the Echo VR Lounge Discord first."

// ResetPasswordHint explains the fix when an account with a password tries to
// sign in with a device link (the backend then rejects the login).
const ResetPasswordHint = "device sign-in only works when the EchoVRCE account has no password: run /reset-password in the Echo VR Lounge Discord, then link again"

func (a *App) devicePath() string { return filepath.Join(a.Dir, DeviceFile) }

// HasAccount reports whether the servers can sign in.
func (a *App) HasAccount() bool { return len(a.Secrets.Missing(a.DeviceLinked())) == 0 }

// DeviceLinked reports whether a device link is stored.
func (a *App) DeviceLinked() bool {
	_, err := os.Stat(a.devicePath())
	return err == nil
}

// LinkDevice runs the echovrce.com/device flow, stores the link, fills in the
// Discord ID from the backend and gives every server the new link.
func (a *App) LinkDevice(ctx context.Context, show func(url, code string, expires time.Time)) (auth.Session, error) {
	c := a.Auth()
	code, err := c.RequestDevice(ctx)
	if err != nil {
		return auth.Session{}, err
	}
	show(code.URL(), code.Code, code.Expires)
	wctx, cancel := context.WithDeadline(ctx, code.Expires)
	defer cancel()
	linked, err := c.WaitDevice(wctx, code, 0)
	if err != nil {
		return auth.Session{}, err
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return auth.Session{}, err
	}
	if err := auth.WriteCredentials(a.devicePath(), linked); err != nil {
		return auth.Session{}, err
	}
	if err := a.fillDiscordID(ctx, linked.Session); err != nil {
		return linked.Session, err
	}
	for _, in := range a.Cfg.Instances {
		if err := a.installDeviceLink(in.Name, true); err != nil {
			return linked.Session, err
		}
	}
	return linked.Session, nil
}

// fillDiscordID stores the account's Discord ID when it is not set yet.
func (a *App) fillDiscordID(ctx context.Context, s auth.Session) error {
	if a.Secrets[config.EnvDiscordID] != "" {
		return nil
	}
	id, err := a.Auth().DiscordID(ctx, s)
	if err != nil {
		return fmt.Errorf("looking up the Discord ID: %w", err)
	}
	a.Secrets[config.EnvDiscordID] = id
	return a.Secrets.Save(a.Dir)
}

// refreshDevice signs in with the stored link and saves the rotated token.
func (a *App) refreshDevice(ctx context.Context) (auth.Session, error) {
	b, err := os.ReadFile(a.devicePath())
	if err != nil {
		return auth.Session{}, fmt.Errorf("%w: %v", ErrNoAccount, err)
	}
	var cred struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(b, &cred); err != nil || cred.RefreshToken == "" {
		return auth.Session{}, fmt.Errorf("%s is not a device link", a.devicePath())
	}
	linked, err := a.Auth().Refresh(ctx, cred.RefreshToken)
	if err != nil {
		return auth.Session{}, err
	}
	if err := auth.WriteCredentials(a.devicePath(), linked); err != nil {
		return auth.Session{}, err
	}
	return linked.Session, a.fillDiscordID(ctx, linked.Session)
}

// installDeviceLink puts the device link where the runtime of one server looks
// for it. Existing links are kept unless overwrite is set: the runtime rotates
// its own copy.
func (a *App) installDeviceLink(name string, overwrite bool) error {
	if !a.DeviceLinked() {
		return nil
	}
	dst := filepath.Join(a.Cfg.InstanceDir(name), "root", "_local", ".credentials.json")
	if _, err := os.Stat(dst); err == nil && !overwrite {
		return nil
	}
	b, err := os.ReadFile(a.devicePath())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	// A 6.x runtime reads its sign-in from the nEVR root, where the supervisor
	// copies _local's only when the root has none: drop the root's old one so
	// the new link reaches it.
	if err := os.Remove(rootCredentials(a.Cfg.InstanceDir(name))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, p := range []string{filepath.Join(a.Cfg.InstanceDir(name), "root"), filepath.Dir(dst), dst} {
		if err := os.Chown(p, a.Cfg.UID, a.Cfg.UID); err != nil && os.Geteuid() == 0 {
			return err
		}
	}
	return nil
}

// UseDeviceLink links this host at echovrce.com/device, drops a stored
// password (device sign-in needs an account without one) and recreates the
// servers with the link.
func (a *App) UseDeviceLink(ctx context.Context, show func(url, code string, expires time.Time), say func(string)) (auth.Session, error) {
	s, err := a.LinkDevice(ctx, show)
	if err != nil {
		return s, fmt.Errorf("%w: %w", ErrDeviceLink, err)
	}
	delete(a.Secrets, config.EnvPassword)
	if err := a.Secrets.Save(a.Dir); err != nil {
		return s, err
	}
	say(fmt.Sprintf("Linked as %s (Discord ID %s); %s", s.Username, a.Secrets[config.EnvDiscordID], s.HostSummary()))
	_, err = a.Apply(ctx, false, say)
	return s, err
}

// UsePassword signs in with a Discord ID and server password, stores them
// only if that works, removes the device link and recreates the servers.
func (a *App) UsePassword(ctx context.Context, discordID, password string, say func(string)) (auth.Session, error) {
	s, err := a.Auth().Login(ctx, discordID, password)
	if err != nil {
		return s, err
	}
	a.Secrets[config.EnvDiscordID], a.Secrets[config.EnvPassword] = discordID, password
	if err := a.Secrets.Save(a.Dir); err != nil {
		return s, err
	}
	if err := os.Remove(a.devicePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	for _, in := range a.Cfg.Instances {
		dir := a.Cfg.InstanceDir(in.Name)
		for _, p := range []string{filepath.Join(dir, "root", "_local", ".credentials.json"), rootCredentials(dir)} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return s, err
			}
		}
	}
	say(fmt.Sprintf("Signed in as %s; %s", s.Username, s.HostSummary()))
	_, err = a.Apply(ctx, false, say)
	return s, err
}

// rootCredentials is the sign-in cache a 6.x runtime reads: in the nEVR root of
// the instance's Wine prefix (the image's user is "container").
func rootCredentials(instanceDir string) string {
	return filepath.Join(game.NevrRoot(filepath.Join(instanceDir, ".wine"), "container"), game.CredentialsName)
}

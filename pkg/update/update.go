// Package update rolls a new nevr-runtime out to a fleet of servers without
// cutting matches short: one idle server is pinned to the new version as a
// canary; only if it registers is "current" switched, which every other server
// picks up at its next match boundary.
package update

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
)

// Fleet is how the updater sees the servers; each product implements it.
type Fleet interface {
	// Statuses returns the servers' current status.
	Statuses(ctx context.Context) ([]status.Status, error)
	// Pin makes one server run tag (restarting it); tag "" undoes the pin.
	Pin(ctx context.Context, instance, tag string) error
}

// Followers is implemented by a fleet that knows which servers run the
// store's current release. Only those are used as canary: a server pinned to
// a release of its own would lose its pin when the canary's pin is undone,
// and would not show how current's servers fare.
type Followers interface {
	// FollowsCurrent returns the instances (as Statuses names them) that
	// run whatever release is current.
	FollowsCurrent(ctx context.Context) (map[string]bool, error)
}

// Policy chooses the target release.
type Policy struct {
	// Channel is one of nEVR's channels (stable, beta, ...: the release its
	// manifest names for it) or "pinned" (Pinned). "pre", evr-docker's old
	// "newest GitHub release", is stable (nevr.ChannelName).
	Channel string
	Pinned  string
}

// Result describes what Run did.
type Result struct {
	From, To string
	Action   string // up_to_date, switched, rolled_back, installed_only
	Canary   string
	Detail   string
}

// Updater runs one update check.
type Updater struct {
	Store  nevr.Store
	Client *nevr.Client
	Fleet  Fleet
	// CanaryTimeout bounds the wait for an idle server and for its registration.
	CanaryTimeout time.Duration
	Poll          time.Duration
	Log           func(string)
}

func (u *Updater) log(f string, a ...any) {
	if u.Log != nil {
		u.Log(fmt.Sprintf(f, a...))
	}
}

// Target returns the release the policy wants.
func (u *Updater) Target(ctx context.Context, p Policy) (nevr.Release, error) {
	c := u.Client
	if c == nil {
		c = &nevr.Client{}
	}
	if p.Channel == "pinned" {
		return c.Find(ctx, p.Pinned)
	}
	return c.Channel(ctx, p.Channel)
}

// Check reports whether an update is available without changing anything.
func (u *Updater) Check(ctx context.Context, p Policy) (current string, target nevr.Release, available bool, err error) {
	current = u.Store.Current()
	target, err = u.Target(ctx, p)
	if err != nil {
		return current, target, false, err
	}
	if inst, err := u.Store.Get(target.Tag); err == nil && inst.Bad != "" {
		return current, target, false, nil // already failed once; wait for a newer release
	}
	return current, target, target.Tag != current, nil
}

// Run installs and rolls out the policy target if it differs from current.
func (u *Updater) Run(ctx context.Context, p Policy) (Result, error) {
	cur, target, available, err := u.Check(ctx, p)
	res := Result{From: cur, To: target.Tag, Action: "up_to_date"}
	if err != nil || !available {
		return res, err
	}
	u.log("installing nevr-runtime %s", target.Tag)
	if _, err := u.Store.Install(ctx, target); err != nil {
		return res, err
	}
	if cur == "" || u.Fleet == nil || u.noFollowers(ctx) {
		res.Action = "switched"
		return res, u.Store.Use(target.Tag)
	}
	timeout := u.CanaryTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	poll := u.Poll
	if poll == 0 {
		poll = 10 * time.Second
	}
	canary, err := u.waitIdle(ctx, timeout, poll)
	if err != nil {
		res.Action, res.Detail = "installed_only", err.Error()
		return res, nil // try again on the next check
	}
	res.Canary = canary
	u.log("canary %s: switching to %s", canary, target.Tag)
	if err := u.Fleet.Pin(ctx, canary, target.Tag); err != nil {
		return res, fmt.Errorf("pinning canary %s: %w", canary, err)
	}
	ok, detail := u.waitRegistered(ctx, canary, target.Tag, timeout, poll)
	if !ok && ctx.Err() != nil {
		// Stopped (the agent restarting), not failed: the release is not
		// marked bad, and the canary still goes back to current.
		res.Action, res.Detail = "installed_only", "stopped while the canary started: "+ctx.Err().Error()
		u.log("canary %s: stopped before it registered with %s; staying on %s", canary, target.Tag, cur)
		unpinCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := u.Fleet.Pin(unpinCtx, canary, ""); err != nil {
			return res, fmt.Errorf("unpinning canary %s: %w", canary, err)
		}
		return res, ctx.Err()
	}
	// Undo the pin either way: on success the canary follows current again.
	if ok {
		if err := u.Store.Use(target.Tag); err != nil {
			return res, err
		}
		res.Action = "switched"
		u.log("canary %s registered with %s; current is now %s", canary, target.Tag, target.Tag)
	} else {
		_ = u.Store.MarkBad(target.Tag, detail)
		res.Action, res.Detail = "rolled_back", detail
		u.log("canary %s failed with %s: %s; staying on %s", canary, target.Tag, detail, cur)
	}
	if err := u.Fleet.Pin(ctx, canary, ""); err != nil {
		return res, fmt.Errorf("unpinning canary %s: %w", canary, err)
	}
	return res, nil
}

// noFollowers reports whether the fleet knows no server runs current: then no
// server can be a canary, and none would notice the switch either.
func (u *Updater) noFollowers(ctx context.Context) bool {
	f, ok := u.Fleet.(Followers)
	if !ok {
		return false
	}
	m, err := f.FollowsCurrent(ctx)
	if err != nil {
		return false
	}
	for _, yes := range m {
		if yes {
			return false
		}
	}
	return true
}

// waitIdle returns a registered server that runs current and is not hosting a
// match.
func (u *Updater) waitIdle(ctx context.Context, timeout, poll time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		sts, err := u.Fleet.Statuses(ctx)
		follows := func(string) bool { return true }
		if f, ok := u.Fleet.(Followers); ok && err == nil {
			var m map[string]bool
			if m, err = f.FollowsCurrent(ctx); err == nil {
				follows = func(in string) bool { return m[in] }
			}
		}
		if err == nil {
			for _, s := range sts {
				if s.State == status.StateRegistered && follows(s.Instance) {
					return s.Instance, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("no idle registered server to use as canary")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (u *Updater) waitRegistered(ctx context.Context, instance, tag string, timeout, poll time.Duration) (bool, string) {
	want := strings.TrimPrefix(tag, "v")
	deadline := time.Now().Add(timeout)
	last := "no status"
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false, ctx.Err().Error()
		case <-time.After(poll):
		}
		sts, err := u.Fleet.Statuses(ctx)
		if err != nil {
			last = err.Error()
			continue
		}
		for _, s := range sts {
			if s.Instance != instance {
				continue
			}
			last = string(s.State)
			if s.LastError != "" {
				last += ": " + s.LastError
			}
			if strings.TrimPrefix(s.RuntimeVersion, "v") != want {
				continue
			}
			switch s.State {
			case status.StateRegistered, status.StateInSession:
				return true, ""
			case status.StateFailed:
				return false, last
			}
		}
	}
	return false, "canary did not register in time (" + last + ")"
}

package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
)

func setDeviceLink(t *testing.T, on bool) {
	t.Helper()
	old := core.DeviceLinkFlow
	core.DeviceLinkFlow = on
	t.Setenv(core.DeviceLinkEnv, "")
	t.Cleanup(func() { core.DeviceLinkFlow = old })
}

// wizardAccountPage opens the wizard and moves past the welcome page.
func wizardAccountPage(t *testing.T) string {
	t.Helper()
	m := newModel(context.Background(), t.TempDir())
	m.startWizard()
	f := pump(m.form, m.form.Init())
	f = pump(f, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} })
	return f.View()
}

// pump feeds cmd's messages to the model until it is quiet. Commands that do
// not answer at once (cursor blinks, ticks) are dropped.
func pump(f *huh.Form, cmd tea.Cmd) *huh.Form {
	queue := run(cmd)
	for i := 0; i < 100 && len(queue) > 0; i++ {
		var msg tea.Msg
		var model huh.Model
		msg, queue = queue[0], queue[1:]
		model, cmd = f.Update(msg)
		f = model.(*huh.Form)
		queue = append(queue, run(cmd)...)
	}
	return f
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(20 * time.Millisecond):
		return nil
	}
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func TestWizardAsksForThePassword(t *testing.T) {
	setDeviceLink(t, false)
	v := wizardAccountPage(t)
	if !strings.Contains(v, "Server password") || strings.Contains(v, "echovrce.com/device") {
		t.Fatalf("without --device-link the wizard must ask for the password, not offer a device link:\n%s", v)
	}
}

func TestWizardOffersTheDeviceLinkWithTheSwitch(t *testing.T) {
	setDeviceLink(t, true)
	v := wizardAccountPage(t)
	if !strings.Contains(v, "echovrce.com/device") || !strings.Contains(v, "server password (needed for nevr-runtime 6.x)") {
		t.Fatalf("with --device-link the wizard offers both, the password first:\n%s", v)
	}
	if strings.Index(v, "server password (needed") > strings.Index(v, "Device link at") {
		t.Fatalf("the password must come first:\n%s", v)
	}
}

func TestAccountScreenWithoutSignIn(t *testing.T) {
	for _, on := range []bool{false, true} {
		setDeviceLink(t, on)
		m := newModel(context.Background(), t.TempDir())
		m.account = &accountInfo{err: core.ErrNoAccount}
		v := m.renderAccount()
		if !strings.Contains(v, "to sign in with your Discord ID and server password") {
			t.Errorf("device link %v: no password sign-in offered:\n%s", on, v)
		}
		if got := strings.Contains(v, "echovrce.com/device"); got != on {
			t.Errorf("device link %v: link offered = %v:\n%s", on, got, v)
		}
		if got := strings.Contains(signInFirst(), "l links"); got != on {
			t.Errorf("device link %v: flash offers l = %v", on, got)
		}
	}
}

func TestAccountScreenIgnoresLWithoutTheSwitch(t *testing.T) {
	setDeviceLink(t, false)
	m := newModel(context.Background(), t.TempDir())
	m.screen = scrAccount
	if _, cmd := m.updateAccount(tea.KeyPressMsg{Code: 'l', Text: "l"}); cmd != nil {
		t.Fatal("l must do nothing without --device-link")
	}
	if m.busyTitle != "" {
		t.Fatalf("a task started: %q", m.busyTitle)
	}
}

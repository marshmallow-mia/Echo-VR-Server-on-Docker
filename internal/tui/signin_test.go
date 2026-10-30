package tui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/core"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/docker"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/status"
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

// The wizard's sign-in defaults to each server's console code; the password
// is the second choice; the host-wide device link only with --device-link.
func TestWizardDefaultsToTheConsoleCode(t *testing.T) {
	setDeviceLink(t, false)
	v := wizardAccountPage(t)
	code, pw := strings.Index(v, "Sign-in code on each server's console"), strings.Index(v, "Discord ID and server password")
	if code < 0 || pw < 0 || code > pw {
		t.Fatalf("the console code must come first, the password second:\n%s", v)
	}
	if !strings.Contains(ansi.Strip(v), "The dashboard shows the link meanwhile.") || !strings.Contains(ansi.Strip(v), "no password stored)") {
		t.Errorf("the note is cut:\n%s", v)
	}
	if !regexp.MustCompile(`> Sign-in code on each server's console`).MatchString(ansi.Strip(v)) {
		t.Errorf("the console code is not preselected:\n%s", v)
	}
	if strings.Contains(v, "Device link") || strings.Contains(v, "Server password") {
		t.Fatalf("without --device-link: no device link, and no password field before it is chosen:\n%s", v)
	}
}

func TestWizardOffersTheDeviceLinkWithTheSwitch(t *testing.T) {
	setDeviceLink(t, true)
	if v := wizardAccountPage(t); !strings.Contains(v, "Device link") {
		t.Fatalf("with --device-link the wizard offers the device link too:\n%s", v)
	}
}

func TestAccountScreenWithTheConsoleCode(t *testing.T) {
	setDeviceLink(t, false)
	m := newModel(context.Background(), t.TempDir())
	m.account = &accountInfo{err: core.ErrSignInByCode}
	v := m.renderAccount()
	if !strings.Contains(v, "echovrce.com/device") || !strings.Contains(v, "to sign in with your Discord ID and server password instead") {
		t.Errorf("console code screen:\n%s", v)
	}
	if strings.Contains(v, "Error") || strings.Contains(v, "link this host") {
		t.Errorf("the console code is not an error, and no device link without the switch:\n%s", v)
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

// A server waiting for its code shows the link on the dashboard.
func TestDashboardShowsTheApprovalLink(t *testing.T) {
	m := newModel(context.Background(), t.TempDir())
	m.servers = []core.Server{{Instance: hostcfg.Instance{Name: "evr-1", Port: 6792},
		Container: &docker.Container{State: "running", Created: time.Now().Add(-time.Minute)},
		Status: &status.Status{State: status.StateAwaitingApproval, UpdatedAt: time.Now(),
			SignInURL: "https://echovrce.com/device?code=TEST", SignInExpires: time.Now().Add(5 * time.Minute)}}}
	m.refreshTable()
	row := strings.Join(m.table.Rows()[0], " ")
	if !strings.Contains(row, "approve code") || !strings.Contains(row, "https://echovrce.com/device?code=TEST") {
		t.Errorf("row: %s", row)
	}
	if d := m.approvalDetail(); !strings.Contains(d, "https://echovrce.com/device?code=TEST") || !strings.Contains(d, "host account") {
		t.Errorf("detail: %q", d)
	}
}

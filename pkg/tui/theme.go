// Package tui holds the look and the widgets shared by the evr-docker and
// evr-ptero terminal UIs, so both tools feel the same.
package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Colors work on dark and light terminals.
var (
	Accent  = lipgloss.Color("#7D56F4")
	Good    = lipgloss.Color("#2E9E4F")
	WarnCol = lipgloss.Color("#C98A00")
	Bad     = lipgloss.Color("#D7263D")
	Muted   = lipgloss.Color("#8A8A8A")
	Info    = lipgloss.Color("#2F80ED")
)

// Styles used across screens.
var (
	Title    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(Accent).Padding(0, 1)
	Subtle   = lipgloss.NewStyle().Foreground(Muted)
	Bold     = lipgloss.NewStyle().Bold(true)
	Error    = lipgloss.NewStyle().Foreground(Bad).Bold(true)
	Success  = lipgloss.NewStyle().Foreground(Good).Bold(true)
	Warning  = lipgloss.NewStyle().Foreground(WarnCol).Bold(true)
	Box      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Accent).Padding(0, 1)
	KeyStyle = lipgloss.NewStyle().Foreground(Accent).Bold(true)
)

// StateStyle colors a server state word.
func StateStyle(state string) lipgloss.Style {
	switch state {
	case "registered":
		return lipgloss.NewStyle().Foreground(Good)
	case "in_session":
		return lipgloss.NewStyle().Foreground(Info).Bold(true)
	case "starting", "stopping", "backoff", "restarting", "created":
		return lipgloss.NewStyle().Foreground(WarnCol)
	case "failed", "exited", "dead":
		return lipgloss.NewStyle().Foreground(Bad).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(Muted)
	}
}

// StateLabel is a friendly name for a state.
func StateLabel(state string) string {
	switch state {
	case "registered":
		return "ready"
	case "in_session":
		return "in match"
	case "backoff":
		return "retrying"
	default:
		return strings.ReplaceAll(state, "_", " ")
	}
}

// Key is one entry of a help bar.
type Key struct{ Key, Help string }

// K builds a Key.
func K(key, help string) Key { return Key{Key: key, Help: help} }

// HelpBar renders "k action · k action".
func HelpBar(keys ...Key) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, KeyStyle.Render(k.Key)+" "+Subtle.Render(k.Help))
	}
	return strings.Join(parts, Subtle.Render(" · "))
}

// Check is a doctor-style result line.
type Check struct {
	Name, Level, Detail, Fix string // Level: ok, warn, fail
}

// Checks renders a list of check results.
func Checks(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		mark := Success.Render("✔")
		switch c.Level {
		case "warn":
			mark = Warning.Render("!")
		case "fail":
			mark = Error.Render("✘")
		}
		fmt.Fprintf(&b, "%s %-15s %s\n", mark, c.Name, c.Detail)
		if c.Fix != "" && c.Level != "ok" {
			fmt.Fprintf(&b, "  %-15s %s\n", "", Subtle.Render("→ "+c.Fix))
		}
	}
	return b.String()
}

// Truncate shortens s to n runes with an ellipsis.
func Truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

package tui

import (
	"regexp"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// LogLineMsg delivers one line to a LogView.
type LogLineMsg struct {
	Source string // which log the line belongs to
	Line   string
}

// LogView shows a growing log with follow mode, a noise filter and search.
type LogView struct {
	vp       viewport.Model
	lines    []string
	follow   bool
	all      bool // show noisy runtime lines too
	filter   string
	editing  bool
	Max      int
	IsNoisy  func(string) bool
	width    int
	height   int
	ansiStrp *regexp.Regexp
}

// NewLogView returns a log view; isNoisy marks lines hidden by default.
func NewLogView(isNoisy func(string) bool) LogView {
	return LogView{vp: viewport.New(), follow: true, Max: 5000, IsNoisy: isNoisy, ansiStrp: regexp.MustCompile(`\x1b\[[0-9;]*m`)}
}

// SetSize sets the viewport size.
func (l *LogView) SetSize(w, h int) {
	l.width, l.height = w, h
	l.vp.SetWidth(w)
	l.vp.SetHeight(max(h-1, 1))
	l.refresh()
}

// Append adds a line.
func (l *LogView) Append(line string) {
	l.lines = append(l.lines, line)
	if len(l.lines) > l.Max {
		l.lines = l.lines[len(l.lines)-l.Max:]
	}
	l.refresh()
}

// Reset clears the log.
func (l *LogView) Reset() { l.lines = nil; l.refresh() }

func (l *LogView) visible() []string {
	out := make([]string, 0, len(l.lines))
	f := strings.ToLower(l.filter)
	for _, ln := range l.lines {
		if !l.all && l.IsNoisy != nil && l.IsNoisy(ln) {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(l.ansiStrp.ReplaceAllString(ln, "")), f) {
			continue
		}
		out = append(out, l.style(ln))
	}
	return out
}

func (l *LogView) style(ln string) string {
	plain := l.ansiStrp.ReplaceAllString(ln, "")
	switch {
	case strings.HasPrefix(plain, "[evr]"):
		return lipgloss.NewStyle().Foreground(Accent).Bold(true).Render(plain)
	case strings.Contains(plain, "FATAL") || strings.Contains(plain, " error "):
		return lipgloss.NewStyle().Foreground(Bad).Render(plain)
	case strings.Contains(plain, " warn "):
		return lipgloss.NewStyle().Foreground(WarnCol).Render(plain)
	}
	return plain
}

func (l *LogView) refresh() {
	l.vp.SetContentLines(l.visible())
	if l.follow {
		l.vp.GotoBottom()
	}
}

// Editing reports whether the filter prompt has focus (so the parent should
// not treat keys as shortcuts).
func (l LogView) Editing() bool { return l.editing }

// Update handles scrolling and the log keys: f follow, a all lines, / filter.
func (l LogView) Update(msg tea.Msg) (LogView, tea.Cmd) {
	switch m := msg.(type) {
	case LogLineMsg:
		l.Append(m.Line)
		return l, nil
	case tea.KeyPressMsg:
		if l.editing {
			switch m.String() {
			case "enter", "esc":
				l.editing = false
			case "backspace":
				if r := []rune(l.filter); len(r) > 0 {
					l.filter = string(r[:len(r)-1])
				}
			default:
				if m.Text != "" {
					l.filter += m.Text
				}
			}
			l.refresh()
			return l, nil
		}
		switch m.String() {
		case "f":
			l.follow = !l.follow
			l.refresh()
			return l, nil
		case "a":
			l.all = !l.all
			l.refresh()
			return l, nil
		case "/":
			l.editing = true
			return l, nil
		case "G", "end":
			l.follow = true
			l.refresh()
			return l, nil
		case "up", "k", "pgup", "home", "g":
			l.follow = false
		}
	}
	var cmd tea.Cmd
	l.vp, cmd = l.vp.Update(msg)
	if l.vp.AtBottom() && !l.follow {
		// scrolling back to the end resumes following
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "down" || k.String() == "j" || k.String() == "pgdown") {
			l.follow = true
		}
	}
	return l, cmd
}

// View renders the log and a status line.
func (l LogView) View() string {
	status := []string{}
	if l.follow {
		status = append(status, Success.Render("following"))
	} else {
		status = append(status, Warning.Render("paused")+Subtle.Render(" (G to follow)"))
	}
	if l.all {
		status = append(status, "all lines")
	} else {
		status = append(status, Subtle.Render("noise hidden (a)"))
	}
	if l.editing {
		status = append(status, "filter: "+l.filter+"█")
	} else if l.filter != "" {
		status = append(status, "filter: "+Bold.Render(l.filter))
	}
	return l.vp.View() + "\n" + strings.Join(status, Subtle.Render(" · "))
}

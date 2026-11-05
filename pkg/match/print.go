package match

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Fprint writes m for a terminal: line, session, both teams, events.
func Fprint(w io.Writer, m Match) {
	state := "last match"
	if m.Active {
		state = "in a match"
	}
	fmt.Fprintf(w, "%s: %s\n", state, Line(m))
	fmt.Fprintf(w, "session:  %s (since %s)\n", dash(m.SessionID), m.StartedAt.Local().Format(time.DateTime))
	fmt.Fprintf(w, "stats API: %s", m.API)
	if m.APIError != "" {
		fmt.Fprintf(w, " (%s)", m.APIError)
	}
	fmt.Fprintln(w)
	if m.Status != "" {
		fmt.Fprintf(w, "status:   %s\n", m.Status)
	}
	team := func(name string, t *Team) {
		if t == nil {
			return
		}
		fmt.Fprintf(w, "\n%s %s\n", strings.ToUpper(name), Points(t.Points, m.Percent))
		for _, p := range t.Players {
			st := Stats{}
			if p.Stats != nil {
				st = *p.Stats
			}
			fmt.Fprintf(w, "  #%-3d %-24s ping %3d  pts %2d  goals %d  assists %d  saves %d  stuns %d  steals %d\n",
				p.Number, p.Name, p.Ping, st.Points, st.Goals, st.Assists, st.Saves, st.Stuns, st.Steals)
		}
	}
	team("blue", m.Blue)
	team("orange", m.Orange)
	if len(m.Spectators) > 0 {
		var names []string
		for _, p := range m.Spectators {
			names = append(names, p.Name)
		}
		fmt.Fprintf(w, "\nspectators: %s\n", strings.Join(names, ", "))
	}
	if len(m.Events) > 0 {
		fmt.Fprintln(w, "\nevents:")
		for _, e := range m.Events {
			fmt.Fprintf(w, "  %s %-9s %s\n", e.At.Local().Format("15:04:05"), e.Clock, e.Text)
		}
	}
}

// FprintHistory lists finished matches for a terminal.
func FprintHistory(w io.Writer, hist []Summary) {
	if len(hist) == 0 {
		return
	}
	fmt.Fprintln(w, "\nFinished matches:")
	for _, h := range hist {
		score := ""
		if h.Source == "session_api" {
			score = fmt.Sprintf("  blue %s – %s orange", Points(h.Blue, h.Percent), Points(h.Orange, h.Percent))
		}
		fmt.Fprintf(w, "  %s  %-26s %7s%s  %d players\n", h.EndedAt.Local().Format("01-02 15:04"),
			Line(Match{GameType: h.GameType, Map: h.Map}), (time.Duration(h.Duration) * time.Second).String(), score, len(h.Players))
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

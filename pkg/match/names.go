package match

import (
	"fmt"
	"strconv"
	"strings"
)

// Display names of game types and levels; others are shown as they are.
var (
	GameTypeNames = map[string]string{
		"echo_arena":          "Arena",
		"echo_arena_private":  "Arena (private)",
		"echo_combat":         "Combat",
		"echo_combat_private": "Combat (private)",
		"social_2.0":          "Lobby",
		"social_2.0_private":  "Lobby (private)",
		"social_2.0_npe":      "Lobby (tutorial)",
	}
	LevelNames = map[string]string{
		"mpl_arena_a":           "Arena",
		"mpl_lobby_b2":          "Lobby",
		"mpl_combat_dyson":      "Dyson",
		"mpl_combat_combustion": "Combustion",
		"mpl_combat_fission":    "Fission",
		"mpl_combat_gauss":      "Surge",
	}
)

// GameTypeName is a game type's display name.
func GameTypeName(s string) string {
	if n, ok := GameTypeNames[strings.ToLower(s)]; ok {
		return n
	}
	return s
}

// LevelName is a level's display name.
func LevelName(s string) string {
	if n, ok := LevelNames[strings.ToLower(s)]; ok {
		return n
	}
	return s
}

// Line is a one-line description of m for terminals, e.g.
// "Arena · blue 2 – 1 orange · 03:12.00 · 8 players".
func Line(m Match) string {
	var parts []string
	mode := GameTypeName(m.GameType)
	if lv := LevelName(m.Map); lv != "" && !strings.EqualFold(lv, mode) {
		mode = strings.TrimSpace(mode + " on " + lv)
	}
	if mode != "" {
		parts = append(parts, mode)
	}
	if m.Blue != nil && m.Orange != nil && !IsLobby(m.GameType) {
		parts = append(parts, fmt.Sprintf("blue %s – %s orange", Points(m.Blue.Points, m.Percent), Points(m.Orange.Points, m.Percent)))
	}
	switch {
	case m.ClockDisplay != "":
		parts = append(parts, m.ClockDisplay)
	case m.Round > 0:
		parts = append(parts, fmt.Sprintf("round %d", m.Round))
	}
	if m.API == APIOn || m.Players > 0 {
		parts = append(parts, fmt.Sprintf("%d players", m.Players))
	}
	if m.Paused != "" {
		parts = append(parts, m.Paused)
	}
	if len(parts) == 0 {
		return "match"
	}
	return strings.Join(parts, " · ")
}

// Points formats a score: goals as a number, combat's capture progress in percent.
func Points(v float64, percent bool) string {
	if percent {
		return fmt.Sprintf("%.0f%%", v)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

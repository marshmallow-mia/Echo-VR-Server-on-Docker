package match

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Session is the part of the game's /session answer that match stats use
// (nevr-api engine/v1/engine_http.proto SessionResponse). Poses, the disc's
// vectors and the throw analysis are left out.
type Session struct {
	ErrCode          int        `json:"err_code"`
	ErrDescription   string     `json:"err_description"`
	SessionID        string     `json:"sessionid"`
	GameStatus       string     `json:"game_status"`
	GameClock        float64    `json:"game_clock"`
	GameClockDisplay string     `json:"game_clock_display"`
	MatchType        string     `json:"match_type"`
	MapName          string     `json:"map_name"`
	PrivateMatch     bool       `json:"private_match"`
	TournamentMatch  bool       `json:"tournament_match"`
	BluePoints       float64    `json:"blue_points"` // arena: points; combat: capture progress in percent
	OrangePoints     float64    `json:"orange_points"`
	Contested        *bool      `json:"contested"` // combat: the capture point is contested
	BlueRoundScore   int        `json:"blue_round_score"`
	OrangeRoundScore int        `json:"orange_round_score"`
	TotalRoundCount  int        `json:"total_round_count"`
	Pause            *Pause     `json:"pause"`
	LastScore        *LastScore `json:"last_score"`
	Disc             *APIDisc   `json:"disc"`
	Teams            []APITeam  `json:"teams"`
	RulesChangedBy   string     `json:"rules_changed_by"`
	RulesChangedAt   uint64     `json:"rules_changed_at"`
	BlueRestart      flag       `json:"blue_team_restart_request"`
	OrangeRestart    flag       `json:"orange_team_restart_request"`
	PayloadCheckpt   *int       `json:"payload_checkpoint"`
	PayloadDistance  *float64   `json:"payload_distance"`
	PayloadDefenders *int       `json:"payload_defenders"`
	PayloadSpeed     *float64   `json:"payload_speed"`
	PayloadMult      *float64   `json:"payload_multiplier"`
}

// Pause is the pause state.
type Pause struct {
	PausedState         string  `json:"paused_state"`
	UnpausedTeam        string  `json:"unpaused_team"`
	PausedRequestedTeam string  `json:"paused_requested_team"`
	UnpausedTimer       float64 `json:"unpaused_timer"`
	PausedTimer         float64 `json:"paused_timer"`
}

// LastScore describes the latest goal.
type LastScore struct {
	DiscSpeed      float64 `json:"disc_speed"`
	Team           string  `json:"team"`
	GoalType       string  `json:"goal_type"`
	PointAmount    int     `json:"point_amount"`
	DistanceThrown float64 `json:"distance_thrown"`
	PersonScored   string  `json:"person_scored"`
	AssistScored   string  `json:"assist_scored"`
}

// APIDisc is the disc's state.
type APIDisc struct {
	Position    []float64 `json:"position"`
	BounceCount int       `json:"bounce_count"`
}

// Pose is a head's or body's position.
type Pose struct {
	Position []float64 `json:"position"`
}

// flag is a bool the game may send as a number.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "true":
		*f = true
	case "false", "null", "0", "0.0":
		*f = false
	default:
		var n float64
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*f = n != 0
	}
	return nil
}

// APITeam is one of /session's teams (blue, orange, spectators).
type APITeam struct {
	Team       string      `json:"team"`
	Possession bool        `json:"possession"`
	Stats      *Stats      `json:"stats"`
	Players    []APIPlayer `json:"players"`
}

// APIPlayer is one player in a team.
type APIPlayer struct {
	Name            string  `json:"name"`
	UserID          uint64  `json:"userid"`
	PlayerID        int     `json:"playerid"`
	Number          int     `json:"number"`
	Level           int     `json:"level"`
	Ping            int     `json:"ping"`
	PacketLossRatio float64 `json:"packetlossratio"`
	Stunned         bool    `json:"stunned"`
	Invulnerable    bool    `json:"invulnerable"`
	Blocking        bool    `json:"blocking"`
	Emote           bool    `json:"is_emote_playing"`
	Arm             string  `json:"Arm"`
	Head            *Pose   `json:"head"`
	Possession      bool    `json:"possession"`
	Weapon          string  `json:"Weapon"`
	Ordnance        string  `json:"Ordnance"`
	TacMod          string  `json:"TacMod"`
	Stats           *Stats  `json:"stats"`
}

// Stats are a player's or a team's counters.
type Stats struct {
	PossessionTime float64 `json:"possession_time"`
	Points         int     `json:"points"`
	Goals          int     `json:"goals"`
	Assists        int     `json:"assists"`
	Saves          int     `json:"saves"`
	Stuns          int     `json:"stuns"`
	Steals         int     `json:"steals"`
	Blocks         int     `json:"blocks"`
	Passes         int     `json:"passes"`
	Catches        int     `json:"catches"`
	Interceptions  int     `json:"interceptions"`
	ShotsTaken     int     `json:"shots_taken"`
}

// ErrDisabled: the game answered but its API setting is off (err_code -2).
var ErrDisabled = errors.New("the game's API setting is off")

// ErrNoSession: the API answered 404, there is no session (yet).
var ErrNoSession = errors.New("no session")

// Client fetches /session from one game.
type Client struct {
	HTTP *http.Client
}

// Fetch asks the game on 127.0.0.1:port for its session.
func (c Client) Fetch(ctx context.Context, port int) (*Session, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/session", port), nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParseSession(resp.StatusCode, body)
}

// ParseSession interprets one /session answer.
func ParseSession(code int, body []byte) (*Session, error) {
	if code == http.StatusNotFound {
		return nil, ErrNoSession
	}
	var s Session
	if err := json.Unmarshal(body, &s); err != nil {
		if code != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", code)
		}
		return nil, fmt.Errorf("unreadable answer: %w", err)
	}
	switch {
	case s.ErrCode == -2 || strings.Contains(s.ErrDescription, "API setting is not enabled"):
		return nil, ErrDisabled
	case s.ErrCode != 0:
		return nil, fmt.Errorf("err_code %d: %s", s.ErrCode, s.ErrDescription)
	case code != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d", code)
	}
	return &s, nil
}

package supervisor

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/match"
)

// Kind is a log event the supervisor reacts to.
type Kind int

const (
	KindNone Kind = iota
	KindRuntimeVersion
	KindLoginSuccess
	KindLoginFailed
	KindAuthenticated
	KindBound
	KindServerDBError
	KindRegistered
	KindSessionStart
	KindSessionEnd
	KindTeardown
	KindShutdown
	KindFatal
	KindSignInCode
	KindSignInEnded
	KindGameType // the session's game type (echo_arena, echo_combat, …)
	KindLevel    // the session's game type and level from the game's lobby
	KindHTTPPort // the game's HTTP API (/session) is listening
	// Match timeline and players from the game's log (they work with the API off).
	KindPhase      // the match's state machine entered Phase
	KindTeam       // the server put Player on Team (join or switch)
	KindSpawn      // Player (Account) spawned
	KindDisconnect // Player disconnected; GameOver: after the match
	KindPingStats  // the server's ping stats of the player in Slot, in ms
	KindPeerGone   // a peer was dropped; Reason says why (left, connection lost)
	// Server health.
	KindServerDB       // the matchmaker's socket connected (OK) or closed; Count = reconnects
	KindServerID       // the matchmaker's id for this server
	KindCrashRecovered // the runtime recovered from a game loop crash; Count so far
)

func (k Kind) String() string {
	return [...]string{"none", "runtime_version", "login_success", "login_failed", "authenticated", "bound",
		"serverdb_error", "registered", "session_start", "session_end", "teardown", "shutdown", "fatal",
		"sign_in_code", "sign_in_ended", "game_type", "level", "http_port", "phase", "team", "spawn", "disconnect",
		"ping_stats", "peer_gone", "serverdb", "server_id", "crash_recovered"}[k]
}

// Reason classifies a fatal error; retryable reasons get a restart with
// backoff, the others stop the instance until its settings change.
type Reason string

const (
	ReasonNone           Reason = ""
	ReasonBadCredentials Reason = "bad_credentials"
	ReasonUDPUnreachable Reason = "udp_unreachable"
	ReasonInvalidConfig  Reason = "invalid_config"
	ReasonRegistration   Reason = "registration_rejected"
	// ReasonRuntimeRefused: the host (6.x) did not load nevr-runtime.dll from the
	// nEVR root (missing, or its signature refused) and stopped with exit 78.
	ReasonRuntimeRefused Reason = "runtime_refused"
	// ReasonSignIn: a server without a password (v6.9.0 on) ended its device
	// sign-in without a token, because the service could not be asked. Retried.
	ReasonSignIn Reason = "sign_in"
	ReasonOther  Reason = "fatal"
)

// Retryable reports whether restarting can help.
func (r Reason) Retryable() bool {
	return r != ReasonBadCredentials && r != ReasonInvalidConfig && r != ReasonRuntimeRefused
}

// Exit codes the 6.x host and runtime use for a stop that repeats on every
// start (nevr-runtime's README and example-config.yaml).
const (
	ExitConfigUnreadable = 64 // -config-file names no readable file
	ExitRuntimeRefused   = 78 // the host refused or could not find nevr-runtime.dll
)

// exitReason classifies an exit code no log line explained.
func exitReason(code int) (Reason, string) {
	switch code {
	case ExitRuntimeRefused:
		return ReasonRuntimeRefused, "exit code 78: the nEVR host refused or could not find nevr-runtime.dll in the nEVR root (see the root's logs/nevr-bootstrap.jsonl)"
	case ExitConfigUnreadable:
		return ReasonInvalidConfig, "exit code 64: the config file named by -config-file could not be read"
	}
	return ReasonNone, ""
}

// Event is one interesting log line.
type Event struct {
	Kind    Kind
	Line    string
	Port    int    // KindBound, KindRegistered
	IP      string // KindRegistered: address Nakama saw
	Session string // KindSessionStart/End
	Version string // KindRuntimeVersion
	URL     string // KindSignInCode: the approval page, code included
	Expires int    // KindSignInCode: seconds the code is valid
	OK      bool   // KindSignInEnded: approved
	Reason  Reason // KindFatal, KindLoginFailed
	// GameType and Level: KindGameType, KindLevel.
	GameType, Level string
	// Match timeline and players.
	Phase    string  // KindPhase: pre_match, round_start, main_loop, point_score, round_over, post_match, …
	Player   string  // KindTeam, KindSpawn, KindDisconnect
	Team     int     // KindTeam: 0 blue, 1 orange, 2 spectator, other: hidden
	Account  string  // KindSpawn: OVR-ORG-123…
	GameOver bool    // KindDisconnect
	Slot     int     // KindPingStats
	PingMs   float64 // KindPingStats: median
	PingMax  float64 // KindPingStats: maximum
	Count    int     // KindServerDB: reconnects; KindCrashRecovered
	ServerID string  // KindServerID
}

var (
	ansiRe       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	versionRe    = regexp.MustCompile(`NEVR-BUILD (\S+)`)
	boundRe      = regexp.MustCompile(`Dedicated: broadcaster initialized at \[[^\]:]*:(\d+)\]`)
	registeredRe = regexp.MustCompile(`Dedicated: registered as \S+ at \[[^/\]]*/([^:\]]+):(\d+)\]`)
	sessionRe    = regexp.MustCompile(`session_id=(\S+)`)
	gameTypeRe   = regexp.MustCompile(`game_type=(\S+)`)
	levelRe      = regexp.MustCompile(`Starting session \(gametype (\S+), level (\S+)\)`)
	// The game's own line, and nevr-runtime's when it moved the listener to a free port.
	httpPortRe = regexp.MustCompile(`(?:Bound HTTP listener to|session API listening on) [0-9.]+:(\d+)`)
	// The game's state machine; some names come with a stray quote ('' or `).
	phaseRe      = regexp.MustCompile(`\[DEBUGPRINT\] enter state '+([a-z_]+)`)
	teamRe       = regexp.MustCompile(`Server Assigning Team for '(.*)': (\d+)$`)
	spawnRe      = regexp.MustCompile(`\[NETGAME\] User (\S+) \((.*)\) spawned$`)
	disconnectRe = regexp.MustCompile(`\[NETGAME\] User '(.*)' disconnected IsServer \d+, gameOver: (\d)`)
	pingRe       = regexp.MustCompile(`\[PINGSTATS\]: entrantid: (\d+) est: [0-9.]+ min: \d+ max: (\d+) std: [0-9.]+ med: (\d+)`)
	peerGoneRe   = regexp.MustCompile(`Destroy Peer \d+ \[[^\]]*\] \(([^)]+)\)`)
	reconnectRe  = regexp.MustCompile(`reconnect_count=(\d+)`)
	serverIDRe   = regexp.MustCompile(`registration success via protobuf: server_id=(\d+)`)
	recoveredRe  = regexp.MustCompile(`game loop crash recovered count=(\d+)`)
)

// Peer drop reasons in KindPeerGone's Reason.
const (
	PeerLeft Reason = "left"            // the player's client said goodbye
	PeerLost Reason = "connection lost" // it stopped answering
)

// hostStop is the host's one-line stop record (6.x), printed as bare JSON:
// {"tag":"NEVR.STOP",...,"code":78,"reason":"runtime_refused","detail":"..."}.
type hostStop struct {
	Tag    string `json:"tag"`
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// signInPrompt is the prompt of a server without a password (v6.9.0 on),
// printed on stderr as bare JSON: {"tag":"NEVR.AUTH","event":"server_sign_in_code",
// "code":"…","url":"…?code=…","expires_in_s":300,"msg":"Approve this server: …"}.
// It is the only line that carries the code; the runtime's log masks it.
type signInPrompt struct {
	Tag     string `json:"tag"`
	Event   string `json:"event"`
	URL     string `json:"url"`
	Expires int    `json:"expires_in_s"`
}

// Clean strips colour codes and a JSONL wrapper so every source yields the same text.
func Clean(raw string) string {
	s := strings.TrimRight(ansiRe.ReplaceAllString(raw, ""), "\r\n")
	if strings.HasPrefix(s, "{") && strings.Contains(s, `"msg"`) {
		var rec struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal([]byte(s), &rec) == nil && rec.Msg != "" {
			s = strings.TrimRight(rec.Msg, "\r\n")
		}
	}
	return s
}

// Parse classifies one log line. Lines that do not matter return KindNone.
func Parse(raw string) Event {
	if s := strings.TrimSpace(ansiRe.ReplaceAllString(raw, "")); strings.HasPrefix(s, "{") && strings.Contains(s, `"server_sign_in_code"`) {
		var p signInPrompt
		if json.Unmarshal([]byte(s), &p) == nil && p.Tag == "NEVR.AUTH" && p.Event == "server_sign_in_code" && p.URL != "" {
			return Event{Kind: KindSignInCode, Line: "approve this server: " + p.URL, URL: p.URL, Expires: p.Expires}
		}
	}
	line := Clean(raw)
	ev := Event{Line: line}
	switch {
	case strings.HasPrefix(line, "{") && strings.Contains(line, `"NEVR.STOP"`):
		var st hostStop
		if json.Unmarshal([]byte(line), &st) == nil && st.Tag == "NEVR.STOP" {
			ev.Kind, ev.Reason = KindFatal, ReasonOther
			if st.Reason == string(ReasonRuntimeRefused) || st.Code == ExitRuntimeRefused {
				ev.Reason = ReasonRuntimeRefused
			}
			if st.Detail != "" {
				ev.Line = "nEVR host stopped the game (" + st.Reason + "): " + st.Detail
			}
		}
	case strings.Contains(line, "NEVR-BUILD "):
		if m := versionRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.Version = KindRuntimeVersion, m[1]
		}
	case strings.Contains(line, "[NEVR.AUTH] server sign-in ended: result="):
		ev.Kind, ev.OK = KindSignInEnded, strings.Contains(line, "result=ok")
	case strings.Contains(line, "[NEVR.WS] LOGIN SUCCESS"):
		ev.Kind = KindLoginSuccess
	case strings.Contains(line, "[NEVR.WS] login failed status="):
		ev.Kind, ev.Reason = KindLoginFailed, ReasonBadCredentials
	case strings.Contains(line, "[NEVR.GAMESERVER] Server authenticated"),
		strings.Contains(line, "ServerDB auth via refreshed OAuth2 token"):
		ev.Kind = KindAuthenticated
	case strings.Contains(line, "Dedicated: broadcaster initialized at"):
		if m := boundRe.FindStringSubmatch(line); m != nil {
			ev.Kind = KindBound
			ev.Port, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(line, "[NEVR.SERVERDB] Connection error"):
		ev.Kind = KindServerDBError
	case strings.Contains(line, "Dedicated: registered as"):
		if m := registeredRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.IP = KindRegistered, m[1]
			ev.Port, _ = strconv.Atoi(m[2])
		}
	case strings.Contains(line, "[NEVR.GAMESERVER] session starting"):
		ev.Kind = KindSessionStart
		if m := sessionRe.FindStringSubmatch(line); m != nil {
			ev.Session = m[1]
		}
	case strings.Contains(line, "session ended by ServerDB"), strings.Contains(line, "Dedicated: session ended"):
		ev.Kind = KindSessionEnd
		if m := sessionRe.FindStringSubmatch(line); m != nil {
			ev.Session = m[1]
		}
	case strings.Contains(line, "session game type stored"):
		if m := gameTypeRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.GameType = KindGameType, m[1]
		}
	case strings.Contains(line, "[NETLOBBY] Starting session (gametype"):
		if m := levelRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.GameType, ev.Level = KindLevel, m[1], m[2]
		}
	case strings.Contains(line, "Bound HTTP listener to"), strings.Contains(line, "session API listening on"):
		if m := httpPortRe.FindStringSubmatch(line); m != nil {
			ev.Kind = KindHTTPPort
			ev.Port, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(line, "[DEBUGPRINT] enter state"):
		if m := phaseRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.Phase = KindPhase, strings.TrimSuffix(m[1], "_enter")
		}
	case strings.Contains(line, "Server Assigning Team for"):
		if m := teamRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.Player = KindTeam, m[1]
			ev.Team, _ = strconv.Atoi(m[2])
		}
	case strings.HasSuffix(line, ") spawned"):
		if m := spawnRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.Account, ev.Player = KindSpawn, m[1], m[2]
		}
	case strings.Contains(line, "disconnected IsServer"):
		if m := disconnectRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.Player, ev.GameOver = KindDisconnect, m[1], m[2] == "1"
		}
	case strings.Contains(line, "[PINGSTATS]"):
		// The client's stats come first and are zero; the server's follow.
		if m := pingRe.FindStringSubmatch(line); m != nil && m[3] != "0" {
			ev.Kind = KindPingStats
			ev.Slot, _ = strconv.Atoi(m[1])
			mx, _ := strconv.Atoi(m[2])
			med, _ := strconv.Atoi(m[3])
			ev.PingMs, ev.PingMax = float64(med)/1000, float64(mx)/1000 // µs
		}
	case strings.Contains(line, "Destroy Peer"):
		if m := peerGoneRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "disconnect received":
				ev.Kind, ev.Reason = KindPeerGone, PeerLeft
			case "disconnect timeout", "communication timeout":
				ev.Kind, ev.Reason = KindPeerGone, PeerLost
			} // "no connect received": a join that never happened
		}
	case strings.Contains(line, "[NEVR.SERVERDB] Connected to ServerDB"):
		ev.Kind, ev.OK = KindServerDB, true
		if m := reconnectRe.FindStringSubmatch(line); m != nil {
			ev.Count, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(line, "[NEVR.SERVERDB] Disconnected from ServerDB"):
		ev.Kind = KindServerDB
		if m := reconnectRe.FindStringSubmatch(line); m != nil {
			ev.Count, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(line, "registration success via protobuf"):
		if m := serverIDRe.FindStringSubmatch(line); m != nil {
			ev.Kind, ev.ServerID = KindServerID, m[1]
		}
	case strings.Contains(line, "game loop crash recovered"):
		if m := recoveredRe.FindStringSubmatch(line); m != nil {
			ev.Kind = KindCrashRecovered
			ev.Count, _ = strconv.Atoi(m[1])
		}
	case strings.Contains(line, "CTRL+C teardown complete"):
		ev.Kind = KindTeardown
	case strings.Contains(line, "Graceful shutdown complete"), strings.Contains(line, "Graceful shutdown initiated"):
		ev.Kind = KindShutdown
	case strings.Contains(line, "[NEVR.FATAL]"):
		ev.Kind, ev.Reason = KindFatal, classifyFatal(line)
	}
	return ev
}

func classifyFatal(line string) Reason {
	switch {
	case strings.Contains(line, "device sign-in ended without a token"):
		return ReasonSignIn
	case strings.Contains(line, "HTTP 401"), strings.Contains(line, "no valid token"),
		strings.Contains(line, "the service refused the credential"):
		return ReasonBadCredentials
	case strings.Contains(line, "registration rejected") && strings.Contains(line, "code=10"):
		return ReasonUDPUnreachable
	case strings.Contains(line, "registration rejected"):
		return ReasonRegistration
	case strings.Contains(line, "config.yaml"), strings.Contains(line, "socket_uri"),
		strings.Contains(line, "missing config keys"), strings.Contains(line, "session_router"):
		return ReasonInvalidConfig
	}
	return ReasonOther
}

// Note is the match tracker's note for a timeline or player event.
func (ev Event) Note() (match.Note, bool) {
	switch ev.Kind {
	case KindPhase:
		return match.Note{Kind: match.NotePhase, Phase: ev.Phase}, true
	case KindTeam:
		return match.Note{Kind: match.NoteTeam, Player: ev.Player, Team: match.TeamName(ev.Team)}, true
	case KindSpawn:
		return match.Note{Kind: match.NoteSpawn, Player: ev.Player, Account: ev.Account}, true
	case KindDisconnect:
		return match.Note{Kind: match.NoteLeave, Player: ev.Player, GameOver: ev.GameOver}, true
	case KindPingStats:
		return match.Note{Kind: match.NotePing, PingMs: ev.PingMs, PingMax: ev.PingMax}, true
	case KindPeerGone:
		return match.Note{Kind: match.NoteGone, Reason: string(ev.Reason)}, true
	}
	return match.Note{}, false
}

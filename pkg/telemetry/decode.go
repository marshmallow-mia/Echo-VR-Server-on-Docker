package telemetry

import (
	"fmt"
	"time"

	tv2 "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/telemetry/v2"
	"google.golang.org/protobuf/proto"
)

// Play kinds: the things a player did that /session only counts.
const (
	PlayStun         = "stun"
	PlaySave         = "save"
	PlaySteal        = "steal"
	PlayInterception = "interception"
	PlayBlock        = "block"
	PlayPass         = "pass"
	PlayShot         = "shot"
	PlayAssist       = "assist"
	PlayGoal         = "goal"
)

// Play is one player's action from the stream.
type Play struct {
	Kind   string
	Player string
	Team   string // blue, orange, spectator, social, ""
	Victim string // PlaySteal: whose disc it was
	Total  int    // the player's count of this kind so far in the match
	Points int    // PlayGoal
}

// Text is a line for the match's play-by-play.
func (p Play) Text() string {
	switch p.Kind {
	case PlayStun:
		return fmt.Sprintf("%s stuns (%d)", p.Player, p.Total)
	case PlaySave:
		return fmt.Sprintf("%s saves (%d)", p.Player, p.Total)
	case PlaySteal:
		if p.Victim != "" {
			return fmt.Sprintf("%s steals the disc from %s", p.Player, p.Victim)
		}
		return p.Player + " steals the disc"
	case PlayInterception:
		return fmt.Sprintf("%s intercepts (%d)", p.Player, p.Total)
	case PlayBlock:
		return fmt.Sprintf("%s blocks (%d)", p.Player, p.Total)
	case PlayShot:
		return fmt.Sprintf("%s shoots (%d)", p.Player, p.Total)
	case PlayPass:
		return fmt.Sprintf("%s passes (%d)", p.Player, p.Total)
	case PlayAssist:
		return fmt.Sprintf("%s assists (%d)", p.Player, p.Total)
	case PlayGoal:
		return fmt.Sprintf("%s scores (%d)", p.Player, p.Total)
	}
	return p.Player + " " + p.Kind
}

// Counts are what a Decoder saw, for logs and measurements.
type Counts struct {
	Headers, Frames, Footers, Events, Bad int
	ByEvent                               map[string]int
	FrameBytes                            int
	GameType, Session                     string
}

// Decoder turns envelopes into plays; it remembers the roster to name slots.
type Decoder struct {
	OnPlay func(Play)
	// Latest, when set, gets every frame's player positions (the live map's
	// source in social lobbies).
	Latest *Latest
	roster map[int32]seat
	Counts Counts
}

type seat struct{ name, team string }

func role(r tv2.Role) string {
	switch r {
	case tv2.Role_ROLE_BLUE_TEAM:
		return "blue"
	case tv2.Role_ROLE_ORANGE_TEAM:
		return "orange"
	case tv2.Role_ROLE_SPECTATOR:
		return "spectator"
	case tv2.Role_ROLE_SOCIAL_PARTICIPANT:
		return "social"
	}
	return ""
}

// Envelope decodes one record.
func (d *Decoder) Envelope(b []byte) error {
	if d.roster == nil {
		d.roster = map[int32]seat{}
	}
	if d.Counts.ByEvent == nil {
		d.Counts.ByEvent = map[string]int{}
	}
	var env tv2.Envelope
	if err := proto.Unmarshal(b, &env); err != nil {
		d.Counts.Bad++
		return err
	}
	switch {
	case env.GetHeader() != nil:
		h := env.GetHeader()
		d.Counts.Headers++
		d.Counts.GameType = h.GetGameType()
		d.roster = map[int32]seat{}
		if d.Latest != nil {
			d.Latest.start(h)
		}
		if a := h.GetEchoArena(); a != nil {
			d.Counts.Session = a.GetSessionId()
			for _, p := range a.GetInitialRoster() {
				d.roster[p.GetSlot()] = seat{p.GetDisplayName(), role(p.GetRole())}
			}
		}
	case env.GetFrame() != nil:
		d.Counts.Frames++
		d.Counts.FrameBytes += len(b)
		if f := env.GetFrame().GetEchoArena(); f != nil {
			for _, e := range f.GetEvents() {
				d.event(e)
			}
			if d.Latest != nil {
				d.Latest.frame(f, d.name, time.Now())
			}
		}
	case env.GetFooter() != nil:
		d.Counts.Footers++
	}
	return nil
}

func (d *Decoder) name(slot int32) seat {
	if s, ok := d.roster[slot]; ok {
		return s
	}
	return seat{name: fmt.Sprintf("slot %d", slot)}
}

func (d *Decoder) event(e *tv2.EchoEvent) {
	d.Counts.Events++
	play := func(kind string, slot int32, total int32) {
		d.Counts.ByEvent[kind]++
		if d.OnPlay == nil {
			return
		}
		s := d.name(slot)
		d.OnPlay(Play{Kind: kind, Player: s.name, Team: s.team, Total: int(total)})
	}
	switch {
	case e.GetPlayerJoined() != nil:
		j := e.GetPlayerJoined()
		d.roster[j.GetSlot()] = seat{j.GetDisplayName(), role(j.GetRole())}
		d.Counts.ByEvent["joined"]++
	case e.GetPlayerSwitchedTeam() != nil:
		sw := e.GetPlayerSwitchedTeam()
		s := d.roster[sw.GetPlayerSlot()]
		s.team = role(sw.GetNewRole())
		d.roster[sw.GetPlayerSlot()] = s
		d.Counts.ByEvent["switched"]++
	case e.GetPlayerLeft() != nil:
		d.Counts.ByEvent["left"]++
	case e.GetPlayerStun() != nil:
		play(PlayStun, e.GetPlayerStun().GetPlayerSlot(), e.GetPlayerStun().GetTotalStuns())
	case e.GetPlayerSave() != nil:
		play(PlaySave, e.GetPlayerSave().GetPlayerSlot(), e.GetPlayerSave().GetTotalSaves())
	case e.GetPlayerSteal() != nil:
		st := e.GetPlayerSteal()
		d.Counts.ByEvent[PlaySteal]++
		if d.OnPlay != nil {
			s := d.name(st.GetPlayerSlot())
			p := Play{Kind: PlaySteal, Player: s.name, Team: s.team, Total: int(st.GetTotalSteals())}
			if st.VictimPlayerSlot != nil {
				p.Victim = d.name(st.GetVictimPlayerSlot()).name
			}
			d.OnPlay(p)
		}
	case e.GetPlayerInterception() != nil:
		play(PlayInterception, e.GetPlayerInterception().GetPlayerSlot(), e.GetPlayerInterception().GetTotalInterceptions())
	case e.GetPlayerBlock() != nil:
		play(PlayBlock, e.GetPlayerBlock().GetPlayerSlot(), e.GetPlayerBlock().GetTotalBlocks())
	case e.GetPlayerPass() != nil:
		play(PlayPass, e.GetPlayerPass().GetPlayerSlot(), e.GetPlayerPass().GetTotalPasses())
	case e.GetPlayerShotTaken() != nil:
		play(PlayShot, e.GetPlayerShotTaken().GetPlayerSlot(), e.GetPlayerShotTaken().GetTotalShots())
	case e.GetPlayerAssist() != nil:
		play(PlayAssist, e.GetPlayerAssist().GetPlayerSlot(), e.GetPlayerAssist().GetTotalAssists())
	case e.GetPlayerGoal() != nil:
		play(PlayGoal, e.GetPlayerGoal().GetPlayerSlot(), e.GetPlayerGoal().GetTotalGoals())
	default:
		d.Counts.ByEvent[fmt.Sprintf("%T", e.GetEvent())]++
	}
}

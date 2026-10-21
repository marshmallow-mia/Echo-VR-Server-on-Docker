package supervisor

import (
	"bufio"
	"os"
	"testing"
)

func kinds(t *testing.T, fixture string) map[Kind][]Event {
	t.Helper()
	f, err := os.Open("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[Kind][]Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if ev := Parse(sc.Text()); ev.Kind != KindNone {
			out[ev.Kind] = append(out[ev.Kind], ev)
		}
	}
	return out
}

func TestParseRegisteredRun(t *testing.T) {
	k := kinds(t, "registered-sigint.jsonl")
	if v := k[KindRuntimeVersion]; len(v) != 1 || v[0].Version != "5.4.2" {
		t.Errorf("version: %+v", v)
	}
	if b := k[KindBound]; len(b) != 1 || b[0].Port != 6795 {
		t.Errorf("bound: %+v", b)
	}
	r := k[KindRegistered]
	if len(r) != 1 || r[0].Port != 6795 || r[0].IP != "168.119.2.94" {
		t.Errorf("registered: %+v", r)
	}
	for _, want := range []Kind{KindLoginSuccess, KindAuthenticated, KindTeardown, KindShutdown} {
		if len(k[want]) == 0 {
			t.Errorf("missing %v", want)
		}
	}
	if len(k[KindFatal])+len(k[KindLoginFailed]) != 0 {
		t.Errorf("unexpected failure events: %+v %+v", k[KindFatal], k[KindLoginFailed])
	}
}

func TestParseFailureRuns(t *testing.T) {
	if k := kinds(t, "serverdb-unreachable.jsonl"); len(k[KindServerDBError]) < 5 || len(k[KindRegistered]) != 0 {
		t.Errorf("serverdb-unreachable: %d errors, %d registrations", len(k[KindServerDBError]), len(k[KindRegistered]))
	}
	k := kinds(t, "no-password.jsonl")
	if lf := k[KindLoginFailed]; len(lf) != 1 || lf[0].Reason != ReasonBadCredentials {
		t.Errorf("no-password: %+v", lf)
	}
}

func TestParseStderrAndFatal(t *testing.T) {
	ev := Parse("\x1b[90m2026-10-21T07:40:57.346Z\x1b[0m \x1b[0minfo\x1b[0m [EVR] Dedicated: registered as 0x0BD39DCACC212972 at [172.17.0.2/168.119.2.94:6792]\x1b[0m")
	if ev.Kind != KindRegistered || ev.Port != 6792 {
		t.Errorf("stderr line: %+v", ev)
	}
	cases := map[string]Reason{
		"[NEVR.FATAL] Server authentication failed — no valid token for ServerDB connection: password auth: <redacted> answered HTTP 401": ReasonBadCredentials,
		"[NEVR.FATAL] GameServer registration rejected by ServerDB: payload_bytes=1 code=10 (Failure)":                                    ReasonUDPUnreachable,
		"[NEVR.FATAL] config.yaml at Z:\\x is invalid: reason=parse line=3 column=1 — refusing":                                           ReasonInvalidConfig,
		"[NEVR.FATAL] game loop crashed": ReasonOther,
	}
	for line, want := range cases {
		if got := Parse(line); got.Kind != KindFatal || got.Reason != want {
			t.Errorf("%q: got %v/%v want %v", line, got.Kind, got.Reason, want)
		}
	}
	if s := Parse("2026 info [NEVR.GAMESERVER] session starting session_id=abc-123 lobby=1"); s.Kind != KindSessionStart || s.Session != "abc-123" {
		t.Errorf("session: %+v", s)
	}
}

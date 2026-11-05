package telemetry

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func record(b []byte) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(b)))
	return append(out, b...)
}

func TestIngestSplitsRecordsAndChecksTheToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	got := make(chan string, 8)
	closed := make(chan int64, 1)
	in := &Ingest{Addr: addr, OnEnvelope: func(b []byte) { got <- string(b) },
		OnClose: func(_ error, n, _ int64, _ time.Duration) { closed <- n }}
	token := in.NewToken()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go in.Serve(ctx)
	time.Sleep(100 * time.Millisecond)

	if _, _, err := websocket.Dial(ctx, in.URI(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer wrong"}}}); err == nil {
		t.Fatal("a wrong token was accepted")
	}
	c, _, err := websocket.Dial(ctx, in.URI(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	// Two records in one message, one split over two.
	two := append(record([]byte("a")), record([]byte("bb"))...)
	third := record([]byte("ccc"))
	for _, m := range [][]byte{two, third[:3], third[3:]} {
		if err := c.Write(ctx, websocket.MessageBinary, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "bb", "ccc"} {
		select {
		case g := <-got:
			if g != want {
				t.Errorf("record %q, want %q", g, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no record %q", want)
		}
	}
	c.Close(websocket.StatusNormalClosure, "")
	if n := <-closed; n != 3 {
		t.Errorf("closed with %d records", n)
	}
	if s := in.Stats(); s.Conns != 1 || s.Envelopes != 3 {
		t.Errorf("stats %+v", s)
	}
}

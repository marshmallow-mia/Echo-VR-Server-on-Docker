// Package telemetry receives nevr-runtime's telemetry stream: the game
// server connects to telemetry.uri (a WebSocket) and sends binary messages of
// [u32 little-endian length][telemetry.v2.Envelope protobuf] records. The
// supervisor points that uri at an Ingest on 127.0.0.1 inside the container.
package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// MaxEnvelope is the largest record accepted; a bigger length means the
// stream is not what this package expects.
const MaxEnvelope = 4 << 20

// DumpMax caps one connection's dump file.
const DumpMax = 64 << 20

// Stats count what the ingest received since it started.
type Stats struct {
	Conns     int64
	Messages  int64
	Envelopes int64
	Bytes     int64
}

// Ingest is a local WebSocket endpoint for the telemetry stream.
type Ingest struct {
	// Addr is where it listens, e.g. 127.0.0.1:6730.
	Addr string
	// OnEnvelope gets every record (the bytes are reused afterwards). It runs
	// on the connection's goroutine.
	OnEnvelope func(env []byte)
	// OnConnect and OnClose report connections (for logs and status).
	OnConnect func()
	OnClose   func(err error, envelopes, bytes int64, d time.Duration)
	// DumpDir, when set, keeps each connection's records in
	// <DumpDir>/<time>.bin as they came (length-prefixed), at most DumpMax.
	DumpDir string

	mu    sync.Mutex
	token string
	stats struct{ conns, messages, envelopes, bytes atomic.Int64 }
}

// NewToken makes a fresh bearer token; the runtime must send it to connect.
func (in *Ingest) NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	in.mu.Lock()
	in.token = t
	in.mu.Unlock()
	return t
}

// URI is the telemetry.uri to give the runtime.
func (in *Ingest) URI() string { return "ws://" + in.Addr + "/ingest" }

// Stats returns the counters.
func (in *Ingest) Stats() Stats {
	return Stats{in.stats.conns.Load(), in.stats.messages.Load(), in.stats.envelopes.Load(), in.stats.bytes.Load()}
}

// Serve listens until ctx ends.
func (in *Ingest) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", in.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: http.HandlerFunc(in.handle), ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (in *Ingest) authorized(r *http.Request) bool {
	in.mu.Lock()
	want := in.token
	in.mu.Unlock()
	got := r.Header.Get("Authorization")
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+want)) == 1
}

func (in *Ingest) handle(w http.ResponseWriter, r *http.Request) {
	if !in.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(MaxEnvelope + 4)
	in.stats.conns.Add(1)
	if in.OnConnect != nil {
		in.OnConnect()
	}
	start := time.Now()
	var dump *os.File
	var dumped int64
	if in.DumpDir != "" {
		if err := os.MkdirAll(in.DumpDir, 0o755); err == nil {
			dump, _ = os.Create(filepath.Join(in.DumpDir, start.UTC().Format("20060102T150405Z")+".bin"))
		}
	}
	if dump != nil {
		defer dump.Close()
	}
	var buf []byte
	var envs, bytes int64
	err = func() error {
		for {
			_, msg, err := c.Read(r.Context())
			if err != nil {
				return err
			}
			in.stats.messages.Add(1)
			in.stats.bytes.Add(int64(len(msg)))
			bytes += int64(len(msg))
			if dump != nil && dumped < DumpMax {
				n, _ := dump.Write(msg)
				dumped += int64(n)
			}
			buf = append(buf, msg...)
			for len(buf) >= 4 {
				n := binary.LittleEndian.Uint32(buf)
				if n > MaxEnvelope {
					return fmt.Errorf("record of %d bytes: not a telemetry stream", n)
				}
				if len(buf) < 4+int(n) {
					break
				}
				envs++
				in.stats.envelopes.Add(1)
				if in.OnEnvelope != nil {
					in.OnEnvelope(buf[4 : 4+n])
				}
				buf = buf[4+n:]
			}
			if len(buf) == 0 {
				buf = buf[:0:0]
			}
		}
	}()
	if in.OnClose != nil {
		in.OnClose(err, envs, bytes, time.Since(start))
	}
}

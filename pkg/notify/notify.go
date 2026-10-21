// Package notify sends operator alerts to a Discord webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Level of an event; it picks the embed color.
type Level int

const (
	Info Level = iota
	Good
	Warning
	Critical
)

// Event kinds; users can filter on them.
const (
	EventServerFailed      = "server_failed"      // stopped on an error that needs a settings change
	EventCrashLoop         = "crash_loop"         // restarting over and over
	EventServerDown        = "server_down"        // container not running
	EventServerRecovered   = "server_recovered"   // back to registered after a problem
	EventUpdateAvailable   = "update_available"   // new runtime found
	EventUpdateApplied     = "update_applied"     // runtime switched
	EventUpdateRolledBack  = "update_rolled_back" // canary failed
	EventCredentialsFailed = "credentials_failed" // backend rejected the account
)

// Event is one alert.
type Event struct {
	Kind   string
	Level  Level
	Title  string
	Text   string
	Fields map[string]string
	Host   string
}

// Notifier posts events to a Discord webhook URL. A zero Notifier (empty URL)
// drops everything, so callers need no nil checks.
type Notifier struct {
	URL    string
	Events []string // allowed kinds; empty allows all
	HTTP   *http.Client
	// MinInterval suppresses repeats of the same kind+title.
	MinInterval time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

// Enabled reports whether events are sent anywhere.
func (n *Notifier) Enabled() bool { return n != nil && n.URL != "" }

func (n *Notifier) allowed(kind string) bool {
	if len(n.Events) == 0 {
		return true
	}
	for _, e := range n.Events {
		if e == kind {
			return true
		}
	}
	return false
}

var colors = map[Level]int{Info: 0x2F80ED, Good: 0x2E9E4F, Warning: 0xC98A00, Critical: 0xD7263D}

// Send posts ev unless it is filtered or a recent duplicate.
func (n *Notifier) Send(ctx context.Context, ev Event) error {
	if !n.Enabled() || !n.allowed(ev.Kind) {
		return nil
	}
	key := ev.Kind + "|" + ev.Title
	n.mu.Lock()
	if n.last == nil {
		n.last = map[string]time.Time{}
	}
	min := n.MinInterval
	if min == 0 {
		min = 10 * time.Minute
	}
	if t, ok := n.last[key]; ok && time.Since(t) < min {
		n.mu.Unlock()
		return nil
	}
	n.last[key] = time.Now()
	n.mu.Unlock()

	type field struct {
		Name   string `json:"name"`
		Value  string `json:"value"`
		Inline bool   `json:"inline"`
	}
	var fields []field
	for k, v := range ev.Fields {
		if v != "" {
			fields = append(fields, field{k, truncate(v, 1000), true})
		}
	}
	embed := map[string]any{
		"title": truncate(ev.Title, 250), "description": truncate(ev.Text, 3500), "color": colors[ev.Level],
		"fields": fields, "timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if ev.Host != "" {
		embed["footer"] = map[string]string{"text": ev.Host}
	}
	body, _ := json.Marshal(map[string]any{"username": "Echo VR servers", "embeds": []any{embed}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := n.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook: %s", resp.Status)
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendFiltersAndDedupes(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	n := &Notifier{URL: srv.URL, Events: []string{EventServerFailed}}
	ctx := context.Background()
	ev := Event{Kind: EventServerFailed, Level: Critical, Title: "echo-1 stopped", Text: "bad", Fields: map[string]string{"Port": "6792", "Empty": ""}}
	for i := 0; i < 3; i++ {
		if err := n.Send(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	n.Send(ctx, Event{Kind: EventServerRecovered, Title: "filtered"})
	if len(got) != 1 {
		t.Fatalf("want 1 message (deduped, filtered), got %d", len(got))
	}
	embed := got[0]["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != "echo-1 stopped" || embed["color"].(float64) != float64(colors[Critical]) || len(embed["fields"].([]any)) != 1 {
		t.Errorf("embed %+v", embed)
	}
	if err := (&Notifier{}).Send(ctx, ev); err != nil {
		t.Errorf("disabled notifier must be a no-op: %v", err)
	}
}

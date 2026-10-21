package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeToken(uid string, memberships map[string]int) string {
	m, _ := json.Marshal(memberships)
	claims, _ := json.Marshal(map[string]any{"uid": uid, "usn": "operator", "exp": time.Now().Add(time.Hour).Unix(), "vrs": map[string]any{"memberships": string(m)}})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
}

func fakeNakama(t *testing.T) *httptest.Server {
	t.Helper()
	polls := 0
	tok := fakeToken("u-1", map[string]int{"g-host": 27, "g-member": 1, "g-suspended": 8 | 32})
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/rpc/account/authenticate/password", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("http_key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["password"] != "pw" {
			// What g.echovrce.com actually returns for a wrong password.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"code":13,"message":"rpc error: code = Unauthenticated desc = Invalid credentials."}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": tok, "refresh_token": "r-1"})
	})
	mux.HandleFunc("/v2/rpc/device/auth/request", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"AJ52","expires_in":300}`))
	})
	mux.HandleFunc("/v2/rpc/device/auth/poll", func(w http.ResponseWriter, r *http.Request) {
		if polls++; polls < 2 {
			w.Write([]byte(`{"status":"pending"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "verified", "access_token": tok, "refresh_token": "r-2", "refresh_token_expires_in": 2592000})
	})
	mux.HandleFunc("/v2/rpc/match", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("http_key") || r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"labels":[{"id":"m2","open":true,"lobby_type":"unassigned","created_at":"2026-10-20T16:11:58Z","broadcaster":{"oper":"u-1","version_lock":"0x134b1272e1c4c0b7","default_region":"de-saxony","country_code":"DE","group_ids":["g-host"]}},
			{"id":"m1","created_at":"2026-10-19T16:11:58Z","broadcaster":{"oper":"u-1"}},{"id":"other","broadcaster":{"oper":"u-2"}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginAndHostGuilds(t *testing.T) {
	c := &Client{HTTPURI: fakeNakama(t).URL, HTTPKey: "key"}
	s, err := c.Login(context.Background(), "1", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if s.UserID != "u-1" || s.Username != "operator" {
		t.Errorf("session %+v", s)
	}
	if g := s.HostGuilds(); len(g) != 1 || g[0] != "g-host" {
		t.Errorf("host guilds %v", g)
	}
	if _, err := c.Login(context.Background(), "1", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("want ErrBadCredentials, got %v", err)
	}
	servers, err := c.MyServers(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].MatchID != "m1" || servers[1].Region != "de-saxony" {
		t.Errorf("servers %+v", servers)
	}
}

func TestDeviceLink(t *testing.T) {
	c := &Client{HTTPURI: fakeNakama(t).URL, HTTPKey: "key"}
	code, err := c.RequestDevice(context.Background())
	if err != nil || code.URL() != "https://echovrce.com/device?code=AJ52" {
		t.Fatalf("%+v %v", code, err)
	}
	linked, err := c.WaitDevice(context.Background(), code, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "_local", ".credentials.json")
	if err := WriteCredentials(p, linked); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	var got map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &got)
	if fi.Mode().Perm() != 0o600 || got["refresh_token"] != "r-2" || got["user_id"] != "u-1" {
		t.Errorf("credentials %v mode %v", got, fi.Mode())
	}
}

// Package auth talks to the EchoVRCE (Nakama) backend on behalf of a server
// operator: checking host credentials, linking a device and listing the
// operator's registered servers.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/config"
)

// Role bits in the token's vrs.memberships (EchoTools/nakama evr_guild_group_roles.go).
const (
	RoleMember     = 1 << 0
	RoleEnforcer   = 1 << 1
	RoleAuditor    = 1 << 2
	RoleServerHost = 1 << 3
	RoleAllocator  = 1 << 4
	RoleSuspended  = 1 << 5
)

// ErrBadCredentials means the backend rejected the Discord ID or password.
var ErrBadCredentials = errors.New("the EchoVRCE backend rejected the Discord ID or password")

// Client calls Nakama RPCs.
type Client struct {
	HTTPURI string // e.g. https://g.echovrce.com:7350
	HTTPKey string
	HTTP    *http.Client
}

// Session is a signed-in operator.
type Session struct {
	Token        string
	RefreshToken string
	UserID       string
	Username     string
	Expires      time.Time
	// Memberships maps guild group IDs to role bits.
	Memberships map[string]int
}

// HostGuilds returns the guilds this account may host servers for (ServerHost
// and not suspended). A server registers even with none, but never gets matches.
func (s Session) HostGuilds() []string {
	var out []string
	for g, bits := range s.Memberships {
		if bits&RoleServerHost != 0 && bits&RoleSuspended == 0 {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// Login signs in with the operator's Discord ID and server password, the same
// call the runtime makes before registering.
func (c *Client) Login(ctx context.Context, discordID, password string) (Session, error) {
	var res struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	status, err := c.rpc(ctx, "account/authenticate/password", "", map[string]string{"discord_id": discordID, "password": password}, &res)
	// The live backend answers bad credentials with HTTP 500 and a gRPC
	// "Unauthenticated" status in the message, not with 401.
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound ||
		(err != nil && strings.Contains(err.Error(), "Unauthenticated")) {
		return Session{}, ErrBadCredentials
	}
	if err != nil {
		return Session{}, err
	}
	return sessionFromToken(res.Token, res.RefreshToken)
}

func sessionFromToken(token, refresh string) (Session, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Session{}, errors.New("backend returned a malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return Session{}, fmt.Errorf("token payload: %w", err)
	}
	var claims struct {
		UID string `json:"uid"`
		USN string `json:"usn"`
		Exp int64  `json:"exp"`
		VRS struct {
			Memberships json.RawMessage `json:"memberships"`
		} `json:"vrs"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Session{}, fmt.Errorf("token claims: %w", err)
	}
	s := Session{Token: token, RefreshToken: refresh, UserID: claims.UID, Username: claims.USN, Expires: time.Unix(claims.Exp, 0)}
	// memberships is a JSON object encoded as a string.
	raw := claims.VRS.Memberships
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		raw = json.RawMessage(asString)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.Memberships); err != nil {
			return Session{}, fmt.Errorf("token memberships: %w", err)
		}
	}
	return s, nil
}

// DeviceCode is a pending echovrce.com/device link.
type DeviceCode struct {
	Code    string
	Expires time.Time
}

// URL is the page the operator opens to approve the code.
func (d DeviceCode) URL() string {
	return "https://echovrce.com/device?code=" + url.QueryEscape(d.Code)
}

// RequestDevice starts a device link. Codes are valid for about five minutes.
func (c *Client) RequestDevice(ctx context.Context) (DeviceCode, error) {
	var res struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if _, err := c.rpc(ctx, "device/auth/request", "", struct{}{}, &res); err != nil {
		return DeviceCode{}, err
	}
	if res.Code == "" {
		return DeviceCode{}, errors.New("backend returned no device code")
	}
	return DeviceCode{Code: res.Code, Expires: time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)}, nil
}

// Linked is the result of an approved device link.
type Linked struct {
	Session        Session
	RefreshExpires time.Time
}

// ErrDeviceExpired means the code was not approved in time.
var ErrDeviceExpired = errors.New("the device code expired before it was approved")

// WaitDevice polls until the code is approved, expires, or ctx ends.
func (c *Client) WaitDevice(ctx context.Context, code DeviceCode, every time.Duration) (Linked, error) {
	if every == 0 {
		every = 3 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		var res struct {
			Status                string `json:"status"`
			AccessToken           string `json:"access_token"`
			RefreshToken          string `json:"refresh_token"`
			RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		}
		if _, err := c.rpc(ctx, "device/auth/poll", "", map[string]string{"code": code.Code}, &res); err != nil {
			return Linked{}, err
		}
		switch res.Status {
		case "verified":
			s, err := sessionFromToken(res.AccessToken, res.RefreshToken)
			if err != nil {
				return Linked{}, err
			}
			return Linked{Session: s, RefreshExpires: time.Now().Add(time.Duration(res.RefreshTokenExpiresIn) * time.Second)}, nil
		case "expired":
			return Linked{}, ErrDeviceExpired
		}
		select {
		case <-ctx.Done():
			return Linked{}, ctx.Err()
		case <-t.C:
		}
	}
}

// Refresh exchanges a device-link refresh token for a new session and a
// rotated refresh token (RPC device/auth/refresh).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Linked, error) {
	var res struct {
		AccessToken           string `json:"access_token"`
		Token                 string `json:"token"`
		RefreshToken          string `json:"refresh_token"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
	}
	status, err := c.rpc(ctx, "device/auth/refresh", "", map[string]string{"refresh_token": refreshToken}, &res)
	if status == http.StatusUnauthorized || (err != nil && strings.Contains(err.Error(), "Unauthenticated")) {
		return Linked{}, errors.New("the device link was rejected or has expired (run the link again)")
	}
	if err != nil {
		return Linked{}, err
	}
	tok := res.AccessToken
	if tok == "" {
		tok = res.Token
	}
	if res.RefreshToken == "" {
		res.RefreshToken = refreshToken
	}
	s, err := sessionFromToken(tok, res.RefreshToken)
	if err != nil {
		return Linked{}, err
	}
	return Linked{Session: s, RefreshExpires: time.Now().Add(time.Duration(res.RefreshTokenExpiresIn) * time.Second)}, nil
}

// DiscordID returns the signed-in account's Discord user ID (its Nakama
// custom ID on EchoVRCE).
func (c *Client) DiscordID(ctx context.Context, s Session) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.HTTPURI, "/")+"/v2/account", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("backend account: %s", resp.Status)
	}
	var acc struct {
		CustomID string `json:"custom_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&acc); err != nil {
		return "", err
	}
	if acc.CustomID == "" || strings.Trim(acc.CustomID, "0123456789") != "" {
		return "", errors.New("the backend account has no Discord ID")
	}
	return acc.CustomID, nil
}

// WriteCredentials stores a refresh token where the runtime looks for it
// (<instance>/_local/.credentials.json). The runtime then signs in to ServerDB
// with it instead of the password and keeps it rotated. The password is still
// needed for the login connection.
func WriteCredentials(path string, l Linked) error {
	b, err := json.Marshal(map[string]any{
		"refresh_token":        l.Session.RefreshToken,
		"refresh_token_expiry": l.RefreshExpires.Unix(),
		"user_id":              l.Session.UserID,
		"username":             l.Session.Username,
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Server is one of the operator's registered servers as the backend lists it.
// The backend hides the endpoint; region is its own geolocation.
type Server struct {
	MatchID     string
	Open        bool
	LobbyType   string
	Mode        string
	Players     int
	CreatedAt   time.Time
	VersionLock string
	Region      string
	CountryCode string
	Guilds      []string
}

// MyServers lists the servers registered with the operator's account.
func (c *Client) MyServers(ctx context.Context, s Session) ([]Server, error) {
	var res struct {
		Labels []struct {
			ID          string    `json:"id"`
			Open        bool      `json:"open"`
			LobbyType   string    `json:"lobby_type"`
			Mode        string    `json:"mode"`
			PlayerCnt   int       `json:"player_count"`
			CreatedAt   time.Time `json:"created_at"`
			Broadcaster struct {
				Oper          string   `json:"oper"`
				VersionLock   string   `json:"version_lock"`
				DefaultRegion string   `json:"default_region"`
				CountryCode   string   `json:"country_code"`
				GroupIDs      []string `json:"group_ids"`
			} `json:"broadcaster"`
		} `json:"labels"`
	}
	// Authenticated RPCs reject the http_key, so only the bearer token is sent.
	if _, err := c.rpc(ctx, "match", s.Token, map[string]string{"query": "+label.broadcaster.oper:" + s.UserID}, &res); err != nil {
		return nil, err
	}
	var out []Server
	for _, l := range res.Labels {
		if l.Broadcaster.Oper != s.UserID {
			continue
		}
		out = append(out, Server{MatchID: l.ID, Open: l.Open, LobbyType: l.LobbyType, Mode: l.Mode, Players: l.PlayerCnt,
			CreatedAt: l.CreatedAt, VersionLock: l.Broadcaster.VersionLock, Region: l.Broadcaster.DefaultRegion,
			CountryCode: l.Broadcaster.CountryCode, Guilds: l.Broadcaster.GroupIDs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// rpc posts body to /v2/rpc/<id>. With a bearer token the http_key is left out.
func (c *Client) rpc(ctx context.Context, id, bearer string, body, out any) (int, error) {
	if c.HTTPURI == "" {
		return 0, errors.New("no backend HTTP URI configured")
	}
	q := url.Values{}
	if bearer == "" {
		key := c.HTTPKey
		if key == "" {
			key = config.DefaultHTTPKey
		}
		q.Set("http_key", key)
	}
	u := strings.TrimRight(c.HTTPURI, "/") + "/v2/rpc/" + id + "?" + q.Encode()
	if q.Encode() == "" {
		u += "unwrap"
	} else {
		u += "&unwrap"
	}
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("backend RPC %s: %s %s", id, resp.Status, e.Message)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("backend RPC %s: %w", id, err)
		}
	}
	return resp.StatusCode, nil
}

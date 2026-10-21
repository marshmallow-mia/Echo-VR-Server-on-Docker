// Package docker is a small client for the parts of the Docker Engine API
// evr-docker needs. It talks to the daemon over its Unix socket.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client is a Docker Engine API client.
type Client struct {
	http *http.Client
	base string
}

// ErrNotFound is returned for missing containers and images.
var ErrNotFound = errors.New("not found")

// New connects to DOCKER_HOST (unix:// only) or /var/run/docker.sock.
func New() (*Client, error) {
	sock := "/var/run/docker.sock"
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		if !strings.HasPrefix(h, "unix://") {
			return nil, fmt.Errorf("DOCKER_HOST=%s: only unix:// sockets are supported", h)
		}
		sock = strings.TrimPrefix(h, "unix://")
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &Client{http: &http.Client{Transport: tr}, base: "http://docker/v1.43"}, nil
}

// APIError is an error response from the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the Docker daemon: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, e.Message)
		}
		return nil, &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	return resp, nil
}

func (c *Client) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.do(ctx, method, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version returns the daemon version, as a connectivity check.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct{ Version string }
	err := c.call(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v.Version, err
}

// ContainerSpec is what evr-docker needs to create a game server container.
type ContainerSpec struct {
	Name     string
	Image    string
	User     string
	Env      []string
	Labels   map[string]string
	Binds    []string // host:container[:ro]
	Ports    []Port
	NanoCPUs int64
	Memory   int64
	// StopTimeout must exceed the supervisor's shutdown handling.
	StopTimeout int
}

// Port publishes container port N on HostIP:N (always 1:1; the game
// advertises the port it bound).
type Port struct {
	Port   int
	Proto  string // udp or tcp
	HostIP string
}

// Create creates a container and returns its ID.
func (c *Client) Create(ctx context.Context, s ContainerSpec) (string, error) {
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range s.Ports {
		k := fmt.Sprintf("%d/%s", p.Port, p.Proto)
		exposed[k] = struct{}{}
		bindings[k] = []map[string]string{{"HostIp": p.HostIP, "HostPort": fmt.Sprint(p.Port)}}
	}
	stop := s.StopTimeout
	body := map[string]any{
		"Image": s.Image, "User": s.User, "Env": s.Env, "Labels": s.Labels,
		"ExposedPorts": exposed, "OpenStdin": true, "StopTimeout": stop,
		"HostConfig": map[string]any{
			"Binds":          s.Binds,
			"PortBindings":   bindings,
			"RestartPolicy":  map[string]any{"Name": "unless-stopped"},
			"ReadonlyRootfs": true,
			"Tmpfs":          map[string]string{"/tmp": "rw,exec,size=256m"},
			"NanoCpus":       s.NanoCPUs,
			"Memory":         s.Memory,
			"LogConfig":      map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "20m", "max-file": "3"}},
			"CapDrop":        []string{"ALL"},
			"SecurityOpt":    []string{"no-new-privileges"},
		},
	}
	var res struct{ ID string }
	err := c.call(ctx, http.MethodPost, "/containers/create", url.Values{"name": {s.Name}}, body, &res)
	return res.ID, err
}

// Start, Stop, Restart and Remove act on a container by name or ID.
func (c *Client) Start(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotModified {
		return nil
	}
	return err
}

func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {fmt.Sprint(int(timeout.Seconds()))}}
	return c.call(ctx, http.MethodPost, "/containers/"+id+"/stop", q, nil, nil)
}

func (c *Client) Restart(ctx context.Context, id string, timeout time.Duration) error {
	q := url.Values{"t": {fmt.Sprint(int(timeout.Seconds()))}}
	return c.call(ctx, http.MethodPost, "/containers/"+id+"/restart", q, nil, nil)
}

func (c *Client) Remove(ctx context.Context, id string, force bool) error {
	return c.call(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {fmt.Sprint(force)}}, nil, nil)
}

// Container is the subset of inspect/list data evr-docker uses.
type Container struct {
	ID      string
	Name    string
	Image   string
	ImageID string
	State   string // created, running, exited, restarting, ...
	Status  string // human readable, e.g. "Up 3 hours"
	Labels  map[string]string
	Env     []string
	Started time.Time
	Created time.Time
	Ports   []Port
	Mounts  []Mount
}

// Mount is a container mount.
type Mount struct{ Source, Destination string }

// ListAll returns every container on the host.
func (c *Client) ListAll(ctx context.Context) ([]Container, error) { return c.list(ctx, "") }

// List returns containers that carry label=value (all states).
func (c *Client) List(ctx context.Context, label string) ([]Container, error) {
	return c.list(ctx, label)
}

func (c *Client) list(ctx context.Context, label string) ([]Container, error) {
	q := url.Values{"all": {"1"}}
	if label != "" {
		filters, _ := json.Marshal(map[string][]string{"label": {label}})
		q.Set("filters", string(filters))
	}
	var raw []struct {
		ID      string `json:"Id"`
		Names   []string
		Image   string
		ImageID string
		State   string
		Status  string
		Labels  map[string]string
		Created int64
	}
	if err := c.call(ctx, http.MethodGet, "/containers/json", q, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = strings.TrimPrefix(r.Names[0], "/")
		}
		out = append(out, Container{ID: r.ID, Name: name, Image: r.Image, ImageID: r.ImageID, State: r.State, Status: r.Status, Labels: r.Labels, Created: time.Unix(r.Created, 0)})
	}
	return out, nil
}

// Inspect returns details of one container.
func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	var raw struct {
		ID     string `json:"Id"`
		Name   string
		Image  string
		Config struct {
			Image  string
			Env    []string
			Labels map[string]string
		}
		State struct {
			Status    string
			StartedAt time.Time
		}
		Mounts []Mount
	}
	if err := c.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &raw); err != nil {
		return Container{}, err
	}
	return Container{ID: raw.ID, Name: strings.TrimPrefix(raw.Name, "/"), Image: raw.Config.Image, ImageID: raw.Image,
		State: raw.State.Status, Labels: raw.Config.Labels, Env: raw.Config.Env, Started: raw.State.StartedAt, Mounts: raw.Mounts}, nil
}

// ImageID returns the local image ID for a reference, or ErrNotFound.
func (c *Client) ImageID(ctx context.Context, ref string) (string, error) {
	var raw struct {
		ID string `json:"Id"`
	}
	err := c.call(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &raw)
	return raw.ID, err
}

// Pull pulls an image, reporting progress lines to progress (may be nil).
func (c *Client) Pull(ctx context.Context, ref string, progress func(string)) error {
	image, tag, _ := strings.Cut(ref, "@")
	q := url.Values{"fromImage": {image}}
	if tag != "" {
		q.Set("tag", tag)
	} else if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		q.Set("fromImage", image[:i])
		q.Set("tag", image[i+1:])
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Status, Progress, ID, Error string
		}
		if err := dec.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, m.Error)
		}
		if progress != nil {
			progress(strings.TrimSpace(m.ID + " " + m.Status + " " + m.Progress))
		}
	}
}

// Logs streams a container's output. With follow it keeps going until ctx
// ends. Containers run without a TTY, so the stream is demultiplexed.
func (c *Client) Logs(ctx context.Context, id string, tail int, follow bool, line func(string)) error {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "follow": {fmt.Sprint(follow)}}
	if tail >= 0 {
		q.Set("tail", fmt.Sprint(tail))
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+id+"/logs", q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(demux(resp.Body, pw)) }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line(sc.Text())
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

// demux strips Docker's 8-byte stream frame headers.
func demux(r io.Reader, w io.Writer) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if _, err := io.CopyN(w, r, int64(n)); err != nil {
			return err
		}
	}
}

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/internal/hostcfg"
	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/v2/pkg/nevr/auth"
)

// Level of a check result.
type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string { return [...]string{"ok", "warn", "fail"}[l] }

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	Level  Level  `json:"-"`
	Result string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func check(name string, l Level, detail, fix string) Check {
	return Check{Name: name, Level: l, Result: l.String(), Detail: detail, Fix: fix}
}

// Doctor checks the host. a may be nil before init; then only host checks run.
func Doctor(ctx context.Context, a *App) []Check {
	var out []Check
	if os.Geteuid() == 0 {
		out = append(out, check("user", OK, "running as root", ""))
	} else {
		out = append(out, check("user", Warn, "not running as root", "run evr-docker with sudo so it can manage /srv/echovr and Docker"))
	}
	if v, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		virt := strings.TrimSpace(string(v))
		if virt == "lxc" {
			out = append(out, check("virtualization", Warn, "running in an LXC container", "Docker in LXC needs nesting=1 and keyctl=1; it works on Proxmox with those set"))
		} else {
			out = append(out, check("virtualization", OK, virt, ""))
		}
	}
	dc, err := dockerVersion(ctx, a)
	if err != nil {
		out = append(out, check("docker", Fail, err.Error(), "install Docker: curl -fsSL https://get.docker.com | sh"))
	} else {
		out = append(out, check("docker", OK, "Docker "+dc, ""))
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		out = append(out, check("rsync", Warn, "rsync is not installed", "apt install rsync (needed to download the game)"))
	}
	if a == nil {
		return append(out, check("config", Fail, hostcfg.ErrNotInitialized.Error(), "evr-docker init"))
	}
	cfg := a.Cfg
	out = append(out, diskCheck(cfg.DataDir))
	if info, err := a.Games(nil).Current(); err != nil {
		out = append(out, check("game", Fail, "game files: "+err.Error(), "evr-docker game install"))
	} else {
		out = append(out, check("game", OK, "build "+info.Build.Version+" at "+info.Dir, ""))
	}
	if cur := a.Store().Current(); cur == "" {
		out = append(out, check("runtime", Fail, "no nevr-runtime selected", "evr-docker runtime install"))
	} else {
		out = append(out, check("runtime", OK, "nevr-runtime "+cur, ""))
	}
	if dc != "" {
		if d, err := a.Docker(); err == nil {
			if _, err := d.ImageID(ctx, cfg.Image); err != nil {
				out = append(out, check("image", Warn, cfg.Image+" is not pulled yet", "it is pulled when the servers are next applied"))
			} else {
				out = append(out, check("image", OK, cfg.Image, ""))
			}
		}
	}
	out = append(out, a.accountCheck(ctx))
	out = append(out, portChecks(ctx, a)...)
	out = append(out, publicIPCheck(ctx, cfg))
	return out
}

func dockerVersion(ctx context.Context, a *App) (string, error) {
	if a == nil {
		a = &App{}
	}
	d, err := a.Docker()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return d.Version(ctx)
}

func diskCheck(dir string) Check {
	p := dir
	for p != "/" {
		if _, err := os.Stat(p); err == nil {
			break
		}
		p = parentDir(p)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(p, &fs); err != nil {
		return check("disk", Warn, err.Error(), "")
	}
	free := float64(fs.Bavail) * float64(fs.Bsize) / (1 << 30)
	detail := fmt.Sprintf("%.0f GB free at %s", free, p)
	if free < 8 {
		return check("disk", Warn, detail, "the game needs about 5 GB plus about 300 MB per server")
	}
	return check("disk", OK, detail, "")
}

func parentDir(p string) string {
	i := strings.LastIndex(strings.TrimSuffix(p, "/"), "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func (a *App) accountCheck(ctx context.Context) Check {
	if m := a.Secrets.Missing(); len(m) > 0 {
		return check("account", Fail, fmt.Sprintf("%v not set", m), "evr-docker account set (Discord ID and server password)")
	}
	if a.SignsInByCode() {
		return check("account", OK, "no password: "+SignInCodeNote, "")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	s, err := a.Login(ctx)
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		return check("account", Fail, err.Error(), "evr-docker account set")
	case err != nil:
		return check("account", Warn, "cannot reach the backend: "+err.Error(), "")
	case !s.MembershipsKnown:
		return check("account", OK, s.Username+" signed in with the device link ("+s.HostSummary()+")", "")
	case len(s.HostGuilds()) == 0:
		return check("account", Warn, s.Username+" signed in, but has the server host role in no guild", "ask a guild admin for the ServerHost role, or servers get no matches")
	default:
		return check("account", OK, fmt.Sprintf("%s can host for %d guild(s)", s.Username, len(s.HostGuilds())), "")
	}
}

// portChecks makes sure ports of instances that are not running are free.
func portChecks(ctx context.Context, a *App) []Check {
	servers, err := a.Servers(ctx)
	if err != nil {
		return nil
	}
	var busy []string
	for _, s := range servers {
		if s.Running() || s.Instance.Port == 0 {
			continue
		}
		addr := net.JoinHostPort(a.Cfg.BindIP, fmt.Sprint(s.Instance.Port))
		if c, err := net.ListenPacket("udp", addr); err != nil {
			busy = append(busy, fmt.Sprintf("%s (%s)", addr, s.Instance.Name))
		} else {
			c.Close()
		}
	}
	if len(busy) > 0 {
		return []Check{check("ports", Fail, "in use by something else: "+strings.Join(busy, ", "), "change the instance port or stop the other program")}
	}
	return []Check{check("ports", OK, fmt.Sprintf("%d server port(s) configured", len(a.Cfg.Instances)), "")}
}

func publicIPCheck(ctx context.Context, cfg *hostcfg.Config) Check {
	ip, err := PublicIP(ctx)
	if err != nil {
		return check("public ip", Warn, "cannot determine the public IP: "+err.Error(), "")
	}
	detail := "public IP " + ip
	if cfg.PublicIP != "" && cfg.PublicIP != ip {
		return check("public ip", Warn, fmt.Sprintf("%s, but the config expects %s", detail, cfg.PublicIP),
			"the backend registers servers at the address they connect from; check outbound NAT")
	}
	if cfg.BindIP != "" && cfg.BindIP != ip {
		detail += fmt.Sprintf("; ports bind to %s, so forward UDP %s to it with the same port numbers", cfg.BindIP, portRange(cfg))
	}
	return check("public ip", OK, detail, "")
}

func portRange(cfg *hostcfg.Config) string {
	if len(cfg.Instances) == 0 {
		return "(no servers yet)"
	}
	lo, hi := cfg.Instances[0].Port, cfg.Instances[0].Port
	for _, in := range cfg.Instances {
		lo, hi = min(lo, in.Port), max(hi, in.Port)
	}
	if lo == hi {
		return fmt.Sprint(lo)
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}

// PublicIP asks a public echo service for this host's egress address.
func PublicIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "curl/8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := net.ParseIP(strings.TrimSpace(string(b))); ip != nil {
			return ip.String(), nil
		}
	}
	return "", errors.New("no answer from api.ipify.org or ifconfig.me")
}

// HostIPs returns the host's IPv4 addresses that game ports can be published
// on, leaving out loopback and container/VM bridges.
func HostIPs() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, n.IP.String())
			}
		}
	}
	return out
}

func virtualIface(name string) bool {
	for _, p := range []string{"docker", "br-", "veth", "pterodactyl", "pelican", "virbr", "cni", "flannel", "cali", "lxc", "tailscale", "zt", "wg"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

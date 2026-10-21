// Package metrics exposes server states to Prometheus.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/marshmallow-mia/Echo-VR-Server-on-Docker/pkg/status"
)

// Sample is one instance as the collector sees it.
type Sample struct {
	Status  status.Status
	Running bool // container/process is up
}

// Source returns the current samples; it is called on every scrape.
type Source func() []Sample

var states = []status.State{status.StateStarting, status.StateRegistered, status.StateInSession, status.StateStopping, status.StateBackoff, status.StateFailed, status.StateStopped}

var (
	descUp       = prometheus.NewDesc("evr_server_up", "1 if the server's container is running.", []string{"instance"}, nil)
	descState    = prometheus.NewDesc("evr_server_state", "1 for the server's current state.", []string{"instance", "state"}, nil)
	descReg      = prometheus.NewDesc("evr_server_registrations_total", "Registrations with the backend since the container started.", []string{"instance"}, nil)
	descSessions = prometheus.NewDesc("evr_server_sessions_total", "Matches hosted since the container started.", []string{"instance"}, nil)
	descRestarts = prometheus.NewDesc("evr_server_restarts_total", "Game process restarts since the container started.", []string{"instance"}, nil)
	descSince    = prometheus.NewDesc("evr_server_state_since_seconds", "Unix time the server entered its current state.", []string{"instance"}, nil)
	descRuntime  = prometheus.NewDesc("evr_server_runtime_info", "nevr-runtime version the server runs.", []string{"instance", "version"}, nil)
)

type collector struct{ src Source }

func (c collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descUp, descState, descReg, descSessions, descRestarts, descSince, descRuntime} {
		ch <- d
	}
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.src() {
		in := s.Status.Instance
		up := 0.0
		if s.Running {
			up = 1
		}
		ch <- prometheus.MustNewConstMetric(descUp, prometheus.GaugeValue, up, in)
		for _, st := range states {
			v := 0.0
			if s.Running && s.Status.State == st || !s.Running && st == status.StateStopped {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(descState, prometheus.GaugeValue, v, in, string(st))
		}
		ch <- prometheus.MustNewConstMetric(descReg, prometheus.CounterValue, float64(s.Status.Registrations), in)
		ch <- prometheus.MustNewConstMetric(descSessions, prometheus.CounterValue, float64(s.Status.SessionsTotal), in)
		ch <- prometheus.MustNewConstMetric(descRestarts, prometheus.CounterValue, float64(s.Status.Restarts), in)
		if !s.Status.Since.IsZero() {
			ch <- prometheus.MustNewConstMetric(descSince, prometheus.GaugeValue, float64(s.Status.Since.Unix()), in)
		}
		if s.Status.RuntimeVersion != "" {
			ch <- prometheus.MustNewConstMetric(descRuntime, prometheus.GaugeValue, 1, in, s.Status.RuntimeVersion)
		}
	}
}

// Handler serves /metrics for src plus Go process metrics.
func Handler(src Source) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector{src}, collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), collectors.NewGoCollector())
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

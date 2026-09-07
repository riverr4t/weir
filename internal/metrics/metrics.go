// Package metrics owns the Prometheus registry (spec §11).
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type M struct {
	PollDuration   *prometheus.HistogramVec
	PollErrors     *prometheus.CounterVec
	SnapshotAge    *prometheus.GaugeVec
	AppUp          *prometheus.GaugeVec
	CleanerStrikes *prometheus.GaugeVec
	CleanerActions *prometheus.CounterVec
	RuleMatches    *prometheus.GaugeVec
	RuleBytes      *prometheus.GaugeVec
	SSEClients     prometheus.Gauge
	Registry       *prometheus.Registry
}

func New() *M {
	r := prometheus.NewRegistry()
	m := &M{
		Registry: r,
		PollDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "weir_poll_duration_seconds",
			Help: "Time per poll.", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10}}, []string{"app", "kind"}),
		PollErrors:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "weir_poll_errors_total", Help: "Failed polls."}, []string{"app", "kind"}),
		SnapshotAge:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_snapshot_age_seconds", Help: "Age of the last good poll."}, []string{"app", "kind"}),
		AppUp:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_app_up", Help: "1 when the app answers."}, []string{"app"}),
		CleanerStrikes: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_cleaner_strikes", Help: "Open strikes by condition."}, []string{"condition"}),
		CleanerActions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "weir_cleaner_actions_total", Help: "Cleaner reports and removals."}, []string{"mode", "condition"}),
		RuleMatches:    prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_rule_matches", Help: "Items matched by the last run."}, []string{"rule"}),
		RuleBytes:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "weir_rule_bytes", Help: "Bytes matched by the last run."}, []string{"rule"}),
		SSEClients:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "weir_sse_clients", Help: "Connected event-stream clients."}),
	}
	r.MustRegister(m.PollDuration, m.PollErrors, m.SnapshotAge, m.AppUp, m.CleanerStrikes, m.CleanerActions, m.RuleMatches, m.RuleBytes, m.SSEClients,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *M) Handler() http.Handler { return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}) }

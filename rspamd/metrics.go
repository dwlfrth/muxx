package rspamd

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// The metrics are created at startup but only registered by Register, called
// when rspamd is configured, so they don't appear on /metrics otherwise.
var (
	metricCheck = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mox_rspamd_check_total",
			Help: "Incoming messages checked with rspamd, by result: the rspamd action applied; fallback when mox's own checks decided because rspamd could not be used; or reused when the result for an earlier recipient of the message was applied because the time limit for checks was reached.",
		},
		[]string{"result"},
	)
	metricCheckDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "mox_rspamd_check_duration_seconds",
			Help:    "Duration of checks with rspamd, including failed checks.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 16, 32},
		},
	)
	metricLearn = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mox_rspamd_learn_total",
			Help: "Messages for training rspamd, by result: ok, already (learned before), refused (by rspamd's conditions), failed, dropped (queue full, or message removed before sending).",
		},
		[]string{"result"},
	)
	metricLearnQueue = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "mox_rspamd_learn_queue_length",
			Help: "Messages waiting to be sent to rspamd for training.",
		},
		func() float64 { return float64(LearnQueueLength()) },
	)
)

var registerOnce sync.Once

// Register registers the metrics. Called at startup when rspamd is configured.
func Register() {
	registerOnce.Do(func() {
		prometheus.MustRegister(metricCheck, metricCheckDuration, metricLearn, metricLearnQueue)
	})
}

// CheckResult counts a check by its result: the rspamd action applied,
// ResultFallback or ResultReused.
func CheckResult(result string) {
	metricCheck.WithLabelValues(result).Inc()
}

// Check results other than rspamd actions.
const (
	// ResultFallback is when mox's own checks decided because rspamd could not be used.
	ResultFallback = "fallback"
	// ResultReused is when the result for an earlier recipient of the message was
	// applied, because the time limit for checks was reached.
	ResultReused = "reused"
)

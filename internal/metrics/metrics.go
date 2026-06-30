package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	JobsEnqueued = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forgequeue_jobs_enqueued_total",
			Help: "Total number of jobs submitted to the queue",
		},
		[]string{"type"},
	)

	JobsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forgequeue_jobs_processed_total",
			Help: "Total number of jobs processed",
		},
		[]string{"type", "status"},
	)

	JobsRetried = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forgequeue_jobs_retried_total",
			Help: "Total retry attempts",
		},
		[]string{"type"},
	)

	JobsDead = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "forgequeue_jobs_dead_total",
			Help: "Total jobs moved to dead letter queue",
		},
	)

	JobDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "forgequeue_job_duration_seconds",
			Help:    "Time taken to process a job",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"type"},
	)

	QueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "forgequeue_queue_depth",
			Help: "Current number of jobs in each queue",
		},
		[]string{"queue"},
	)

	LockAcquisitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "forgequeue_lock_acquisitions_total",
			Help: "Total lock acquisition attempts",
		},
		[]string{"lock_name", "status"},
	)

	ReaperSweeps = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "forgequeue_reaper_sweeps_total",
			Help: "Total reaper sweep cycles",
		},
	)

	ReaperRescued = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "forgequeue_reaper_rescued_total",
			Help: "Total jobs rescued by the reaper",
		},
	)
)

func init() {
	prometheus.MustRegister(
		JobsEnqueued,
		JobsProcessed,
		JobsRetried,
		JobsDead,
		JobDuration,
		QueueDepth,
		LockAcquisitions,
		ReaperSweeps,
		ReaperRescued,
	)
}

func Handler() http.Handler {
	return promhttp.Handler()
}

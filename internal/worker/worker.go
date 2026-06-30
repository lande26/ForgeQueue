package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lande26/ForgeQueue/internal/config"
	"github.com/lande26/ForgeQueue/internal/lock"
	"github.com/lande26/ForgeQueue/internal/metrics"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/lande26/ForgeQueue/internal/task"
	"github.com/redis/go-redis/v9"
)

// HandlerFunc is the signature for a job processing function.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) error

// Registry maps job types to their handlers.
type Registry map[string]HandlerFunc

// Worker represents a single job consumer.
type Worker struct {
	id       int
	rdb      *redis.Client
	q        *queue.Queue
	locker   *lock.Lock
	registry Registry
	cfg      *config.Config
	logger   *slog.Logger
}

func NewWorker(id int, rdb *redis.Client, q *queue.Queue, locker *lock.Lock, registry Registry, cfg *config.Config) *Worker {
	return &Worker{
		id:       id,
		rdb:      rdb,
		q:        q,
		locker:   locker,
		registry: registry,
		cfg:      cfg,
		logger:   slog.With("worker_id", id),
	}
}

// Run starts the worker loop.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("worker started")
	
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("worker stopping")
			return
		default:
			// 1. Dequeue (blocks until job available or timeout)
			jobID, err := w.q.Dequeue(ctx, 5*time.Second)
			if err != nil {
				w.logger.Error("dequeue error", "error", err)
				time.Sleep(1 * time.Second) // backoff on error
				continue
			}
			if jobID == "" {
				continue // timeout, try again
			}

			w.processJob(ctx, jobID)
		}
	}
}

func (w *Worker) processJob(ctx context.Context, jobID string) {
	// 2. Acquire per-job lock to prevent concurrent processing of the same job
	acquiredLock, err := w.locker.Acquire(ctx, "job:"+jobID)
	if err != nil {
		w.logger.Debug("could not acquire lock for job (another worker may have it)", "job_id", jobID, "error", err)
		return
	}
	defer func() {
		// Release lock when done
		if err := acquiredLock.Release(context.Background()); err != nil {
			w.logger.Warn("failed to release job lock", "job_id", jobID, "error", err)
		}
	}()

	// 3. Start heartbeat
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go w.heartbeat(heartbeatCtx, jobID)

	// 4. Load metadata
	job, err := w.q.GetJob(ctx, jobID)
	if err != nil {
		w.logger.Error("failed to load job metadata", "job_id", jobID, "error", err)
		// Try to fail the job if possible
		w.q.Fail(context.Background(), jobID, fmt.Sprintf("metadata load error: %v", err))
		return
	}

	w.logger.Info("processing job", "job_id", jobID, "type", job.Type, "retry", job.RetryCount)
	start := time.Now()

	// 5. Execute with timeout
	execCtx, cancelExec := context.WithTimeout(ctx, w.cfg.JobTimeout)
	defer cancelExec()

	execErr := w.execute(execCtx, job)
	duration := time.Since(start)

	// Update metrics
	metrics.JobDuration.WithLabelValues(job.Type).Observe(duration.Seconds())

	// 6. Handle result
	if execErr != nil {
		w.logger.Error("job execution failed", "job_id", jobID, "error", execErr)
		metrics.JobsProcessed.WithLabelValues(job.Type, "failed").Inc()
		if err := w.q.Fail(context.Background(), jobID, execErr.Error()); err != nil {
			w.logger.Error("failed to mark job as failed in queue", "job_id", jobID, "error", err)
		}
	} else {
		w.logger.Info("job execution completed", "job_id", jobID, "duration", duration)
		metrics.JobsProcessed.WithLabelValues(job.Type, "success").Inc()
		if err := w.q.Complete(context.Background(), jobID); err != nil {
			w.logger.Error("failed to mark job as complete in queue", "job_id", jobID, "error", err)
		}
	}
}

func (w *Worker) execute(ctx context.Context, job *task.Job) error {
	handler, ok := w.registry[job.Type]
	if !ok {
		return fmt.Errorf("no handler registered for job type: %s", job.Type)
	}

	// For a real implementation, you might want to pass the fencing token 
	// to the handler so it can safely write to external systems.
	return handler(ctx, job.Payload)
}

func (w *Worker) heartbeat(ctx context.Context, jobID string) {
	ticker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer ticker.Stop()

	jobKey := task.JobKey(jobID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := w.rdb.HSet(ctx, jobKey, "heartbeat_at", time.Now().Unix()).Err()
			if err != nil {
				w.logger.Warn("failed to update job heartbeat", "job_id", jobID, "error", err)
			}
		}
	}
}

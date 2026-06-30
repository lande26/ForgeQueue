package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/lande26/ForgeQueue/internal/config"
	"github.com/lande26/ForgeQueue/internal/lock"
	"github.com/lande26/ForgeQueue/internal/metrics"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/lande26/ForgeQueue/internal/task"
	"github.com/redis/go-redis/v9"
)

// Reaper is responsible for finding jobs that have stalled in the processing queue
// (e.g. because a worker crashed) and returning them to the pending queue.
type Reaper struct {
	rdb    *redis.Client
	locker *lock.Lock
	cfg    *config.Config
	logger *slog.Logger
}

func NewReaper(rdb *redis.Client, locker *lock.Lock, cfg *config.Config) *Reaper {
	return &Reaper{
		rdb:    rdb,
		locker: locker,
		cfg:    cfg,
		logger: slog.With("component", "reaper"),
	}
}

// Run starts the reaper loop.
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.ReaperInterval)
	defer ticker.Stop()

	r.logger.Info("reaper started", 
		"interval", r.cfg.ReaperInterval,
		"staleness_threshold", r.cfg.StalenessThreshold,
	)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reaper stopping")
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *Reaper) sweep(ctx context.Context) {
	// 1. Acquire lock: forgequeue:lock:reaper-sweep
	// If lock is held by another Reaper instance → skip this cycle
	acquiredLock, err := r.locker.Acquire(ctx, "reaper-sweep")
	if err != nil {
		r.logger.Debug("another reaper is sweeping, skipping")
		return
	}
	
	defer func() {
		if err := acquiredLock.Release(context.Background()); err != nil {
			r.logger.Warn("failed to release sweep lock", "error", err)
		}
	}()

	// 2. Get all job IDs currently in processing
	jobIDs, err := r.rdb.LRange(ctx, task.ProcessingQueue, 0, -1).Result()
	if err != nil {
		r.logger.Error("failed to list processing queue", "error", err)
		return
	}

	rescued := 0
	dead := 0

	now := time.Now().Unix()

	// 3. For each job ID, execute the Lua requeue script
	for _, jobID := range jobIDs {
		jobKey := task.JobKey(jobID)
		
		keys := []string{
			jobKey,
			task.ProcessingQueue,
			task.PendingQueue,
			task.DeadQueue,
		}
		
		args := []interface{}{
			r.cfg.StalenessThreshold.Seconds(),
			now,
			jobID,
		}

		result, err := queue.ReaperRequeueScript.Run(ctx, r.rdb, keys, args...).Int()
		if err != nil {
			r.logger.Error("failed to execute requeue script", "job_id", jobID, "error", err)
			continue
		}

		switch result {
		case 1:
			rescued++
			r.logger.Info("rescued stale job", "job_id", jobID)
		case 2:
			dead++
			r.logger.Warn("job moved to dead letter queue", "job_id", jobID)
		}
	}

	// 4. Update metrics
	metrics.ReaperSweeps.Inc()
	if rescued > 0 {
		metrics.ReaperRescued.Add(float64(rescued))
	}
	if dead > 0 {
		metrics.JobsDead.Add(float64(dead))
	}

	r.logger.Info("sweep complete", "checked", len(jobIDs), "rescued", rescued, "dead", dead)
}

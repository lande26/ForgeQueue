package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lande26/ForgeQueue/internal/metrics"
	"github.com/lande26/ForgeQueue/internal/task"
	"github.com/redis/go-redis/v9"
)

var (
	ErrDuplicateJob = errors.New("job with this idempotency key already exists")
	ErrJobNotFound  = errors.New("job not found")
)

type Queue struct {
	rdb *redis.Client
}

func NewQueue(rdb *redis.Client) *Queue {
	return &Queue{rdb: rdb}
}

// Enqueue adds a new job to the pending queue.
func (q *Queue) Enqueue(ctx context.Context, job *task.Job) error {
	// 1. Idempotency check if key provided
	if job.IdempotencyKey != "" {
		idemKey := task.IdempotencyRedisKey(job.IdempotencyKey)
		ok, err := q.rdb.SetNX(ctx, idemKey, job.ID, 24*time.Hour).Result()
		if err != nil {
			return fmt.Errorf("idempotency check error: %w", err)
		}
		if !ok {
			existingID, _ := q.rdb.Get(ctx, idemKey).Result()
			return fmt.Errorf("%w: %s", ErrDuplicateJob, existingID)
		}
	}

	// 2. Write job metadata and push to pending list via pipeline
	pipe := q.rdb.Pipeline()
	pipe.HSet(ctx, job.RedisKey(), job.ToRedisHash())
	pipe.LPush(ctx, task.PendingQueue, job.ID)
	
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("enqueue error: %w", err)
	}

	metrics.JobsEnqueued.WithLabelValues(job.Type).Inc()
	return nil
}

// Dequeue atomically moves a job from pending to processing and returns its ID.
// Blocks up to timeout waiting for a job.
func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration) (string, error) {
	// BLMOVE pending processing RIGHT LEFT {timeout}
	result, err := q.rdb.BLMove(ctx, task.PendingQueue, task.ProcessingQueue, "RIGHT", "LEFT", timeout).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", nil // Timeout reached, no job available
		}
		return "", fmt.Errorf("dequeue error: %w", err)
	}

	jobID := result

	// Update job state
	now := time.Now().Unix()
	_, err = q.rdb.HSet(ctx, task.JobKey(jobID), 
		"status", string(task.StatusProcessing),
		"updated_at", now,
	).Result()
	if err != nil {
		// Log error but proceed, as job is already in processing list
		return jobID, fmt.Errorf("failed to update job state after dequeue: %w", err)
	}

	return jobID, nil
}

// Complete marks a job as successfully finished.
func (q *Queue) Complete(ctx context.Context, jobID string) error {
	pipe := q.rdb.Pipeline()
	pipe.LRem(ctx, task.ProcessingQueue, 1, jobID)
	pipe.HSet(ctx, task.JobKey(jobID), 
		"status", string(task.StatusCompleted),
		"updated_at", time.Now().Unix(),
	)
	
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("complete job error: %w", err)
	}
	return nil
}

// Fail handles a job failure, scheduling it for retry or moving it to dead letter.
func (q *Queue) Fail(ctx context.Context, jobID string, errMsg string) error {
	// 1. Load job to check retry budget
	job, err := q.GetJob(ctx, jobID)
	if err != nil {
		return err
	}

	pipe := q.rdb.Pipeline()
	pipe.LRem(ctx, task.ProcessingQueue, 1, jobID)

	now := time.Now().Unix()

	if job.HasRetriesLeft() {
		// Retry
		pipe.HSet(ctx, job.RedisKey(),
			"status", string(task.StatusPending),
			"retry_count", job.RetryCount+1,
			"last_error", errMsg,
			"updated_at", now,
		)
		pipe.LPush(ctx, task.PendingQueue, jobID)
		metrics.JobsRetried.WithLabelValues(job.Type).Inc()
	} else {
		// Dead
		pipe.HSet(ctx, job.RedisKey(),
			"status", string(task.StatusDead),
			"last_error", errMsg,
			"updated_at", now,
		)
		pipe.LPush(ctx, task.DeadQueue, jobID)
		metrics.JobsDead.Inc()
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("fail job error: %w", err)
	}
	
	return nil
}

// GetJob fetches the current metadata for a job.
func (q *Queue) GetJob(ctx context.Context, jobID string) (*task.Job, error) {
	data, err := q.rdb.HGetAll(ctx, task.JobKey(jobID)).Result()
	if err != nil {
		return nil, fmt.Errorf("get job error: %w", err)
	}
	if len(data) == 0 {
		return nil, ErrJobNotFound
	}
	
	return task.FromRedisHash(data)
}

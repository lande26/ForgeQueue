package task

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// JobStatus represents the current state of a job in the queue.
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
	StatusDead       JobStatus = "dead"
)

// Job represents a unit of work in the queue.
type Job struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	Status         JobStatus       `json:"status"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	HeartbeatAt    int64           `json:"heartbeat_at"`
	RetryCount     int             `json:"retry_count"`
	MaxRetries     int             `json:"max_retries"`
	LastError      string          `json:"last_error,omitempty"`
}

// RedisKey returns the Redis hash key for this job.
func (j *Job) RedisKey() string {
	return fmt.Sprintf("forgequeue:job:%s", j.ID)
}

// IsTerminal returns true if the job is in a final state.
func (j *Job) IsTerminal() bool {
	return j.Status == StatusCompleted || j.Status == StatusDead
}

// HasRetriesLeft returns true if the job can still be retried.
func (j *Job) HasRetriesLeft() bool {
	return j.RetryCount < j.MaxRetries
}

// ToRedisHash flattens the job into a map suitable for HSET.
func (j *Job) ToRedisHash() map[string]interface{} {
	return map[string]interface{}{
		"id":              j.ID,
		"type":            j.Type,
		"payload":         string(j.Payload),
		"status":          string(j.Status),
		"idempotency_key": j.IdempotencyKey,
		"created_at":      j.CreatedAt,
		"updated_at":      j.UpdatedAt,
		"heartbeat_at":    j.HeartbeatAt,
		"retry_count":     j.RetryCount,
		"max_retries":     j.MaxRetries,
		"last_error":      j.LastError,
	}
}

// FromRedisHash reconstructs a Job from an HGETALL result.
func FromRedisHash(data map[string]string) (*Job, error) {
	if data["id"] == "" {
		return nil, fmt.Errorf("job not found")
	}

	createdAt, _ := strconv.ParseInt(data["created_at"], 10, 64)
	updatedAt, _ := strconv.ParseInt(data["updated_at"], 10, 64)
	heartbeatAt, _ := strconv.ParseInt(data["heartbeat_at"], 10, 64)
	retryCount, _ := strconv.Atoi(data["retry_count"])
	maxRetries, _ := strconv.Atoi(data["max_retries"])

	return &Job{
		ID:             data["id"],
		Type:           data["type"],
		Payload:        json.RawMessage(data["payload"]),
		Status:         JobStatus(data["status"]),
		IdempotencyKey: data["idempotency_key"],
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		HeartbeatAt:    heartbeatAt,
		RetryCount:     retryCount,
		MaxRetries:     maxRetries,
		LastError:      data["last_error"],
	}, nil
}

// NewJob creates a new Job with default values.
func NewJob(id, jobType string, payload json.RawMessage, maxRetries int, idempotencyKey string) *Job {
	now := time.Now().Unix()
	return &Job{
		ID:             id,
		Type:           jobType,
		Payload:        payload,
		Status:         StatusPending,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      now,
		UpdatedAt:      now,
		HeartbeatAt:    now,
		RetryCount:     0,
		MaxRetries:     maxRetries,
		LastError:      "",
	}
}

// Redis key constants
const (
	PendingQueue    = "forgequeue:pending"
	ProcessingQueue = "forgequeue:processing"
	DeadQueue       = "forgequeue:dead"
)

// JobKey returns the Redis hash key for a given job ID.
func JobKey(jobID string) string {
	return fmt.Sprintf("forgequeue:job:%s", jobID)
}

// IdempotencyKey returns the Redis key for deduplication.
func IdempotencyRedisKey(key string) string {
	return fmt.Sprintf("forgequeue:idempotency:%s", key)
}

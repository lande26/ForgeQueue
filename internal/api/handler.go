package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/lande26/ForgeQueue/internal/task"
	"github.com/redis/go-redis/v9"
)

type Handler struct {
	q   *queue.Queue
	rdb *redis.Client
}

func NewHandler(q *queue.Queue, rdb *redis.Client) *Handler {
	return &Handler{
		q:   q,
		rdb: rdb,
	}
}

type CreateJobRequest struct {
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	MaxRetries     int             `json:"max_retries"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type CreateJobResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// CreateJob handles POST /jobs
func (h *Handler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req CreateJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Type == "" {
		h.respondError(w, http.StatusBadRequest, "type is required")
		return
	}

	if req.MaxRetries < 0 {
		req.MaxRetries = 3 // default
	}

	jobID := uuid.New().String()
	job := task.NewJob(jobID, req.Type, req.Payload, req.MaxRetries, req.IdempotencyKey)

	err := h.q.Enqueue(r.Context(), job)
	if err != nil {
		if errors.Is(err, queue.ErrDuplicateJob) {
			h.respondJSON(w, http.StatusConflict, CreateJobResponse{
				ID:      err.Error()[len(err.Error())-36:], // Extract ID from error for simplicity
				Status:  string(task.StatusPending),
				Message: "job already exists for this idempotency key",
			})
			return
		}
		h.respondError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	h.respondJSON(w, http.StatusCreated, CreateJobResponse{
		ID:     jobID,
		Status: string(task.StatusPending),
	})
}

// GetJob handles GET /jobs/{id}
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		h.respondError(w, http.StatusBadRequest, "job ID is required")
		return
	}

	job, err := h.q.GetJob(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, queue.ErrJobNotFound) {
			h.respondError(w, http.StatusNotFound, "job not found")
			return
		}
		h.respondError(w, http.StatusInternalServerError, "failed to get job")
		return
	}

	h.respondJSON(w, http.StatusOK, job)
}

type QueueStatsResponse struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Dead       int64 `json:"dead"`
}

// GetQueueStats handles GET /queues/stats
func (h *Handler) GetQueueStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	
	pipe := h.rdb.Pipeline()
	pendingCmd := pipe.LLen(ctx, task.PendingQueue)
	processingCmd := pipe.LLen(ctx, task.ProcessingQueue)
	deadCmd := pipe.LLen(ctx, task.DeadQueue)
	
	_, err := pipe.Exec(ctx)
	if err != nil {
		h.respondError(w, http.StatusInternalServerError, "failed to get queue stats")
		return
	}

	stats := QueueStatsResponse{
		Pending:    pendingCmd.Val(),
		Processing: processingCmd.Val(),
		Dead:       deadCmd.Val(),
	}

	h.respondJSON(w, http.StatusOK, stats)
}

func (h *Handler) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) respondError(w http.ResponseWriter, status int, message string) {
	h.respondJSON(w, status, map[string]string{"error": message})
}

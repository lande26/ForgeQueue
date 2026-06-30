package api

import (
	"net/http"

	"github.com/lande26/ForgeQueue/internal/metrics"
)

// NewRouter creates a new HTTP multiplexer with all routes registered.
func NewRouter(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	// API Routes
	mux.HandleFunc("POST /jobs", h.CreateJob)
	mux.HandleFunc("GET /jobs/{id}", h.GetJob)
	mux.HandleFunc("GET /queues/stats", h.GetQueueStats)

	// System Routes
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	return mux
}

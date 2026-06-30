package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lande26/ForgeQueue/internal/config"
	"github.com/lande26/ForgeQueue/internal/lock"
	"github.com/lande26/ForgeQueue/internal/logger"
	"github.com/lande26/ForgeQueue/internal/metrics"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/lande26/ForgeQueue/internal/worker"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := config.Load()
	log := logger.Init("worker")

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Error("failed to connect to redis", "addr", cfg.RedisAddr, "error", err)
		os.Exit(1)
	}
	log.Info("connected to redis", "addr", cfg.RedisAddr)

	hostname, _ := os.Hostname()
	ownerID := fmt.Sprintf("worker-%s-%d", hostname, os.Getpid())
	
	q := queue.NewQueue(rdb)
	locker := lock.NewLock(rdb, ownerID, 30*time.Second)

	// Register job handlers
	registry := make(worker.Registry)
	registry["echo"] = func(ctx context.Context, payload json.RawMessage) error {
		log.Info("echo handler called", "payload", string(payload))
		return nil
	}
	registry["sleep"] = func(ctx context.Context, payload json.RawMessage) error {
		var req struct {
			Duration int `json:"duration"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return err
		}
		time.Sleep(time.Duration(req.Duration) * time.Second)
		return nil
	}
	registry["fail"] = func(ctx context.Context, payload json.RawMessage) error {
		return fmt.Errorf("intentional failure for testing")
	}

	// Start worker pool
	log.Info("worker pool starting", "concurrency", cfg.WorkerConcurrency)
	pool := worker.NewPool(rdb, q, locker, registry, cfg)
	pool.Start(ctx)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Metrics server
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())
	metricsSrv := &http.Server{
		Addr:    ":" + cfg.MetricsPort,
		Handler: metricsMux,
	}
	go metricsSrv.ListenAndServe()

	// Block until shutdown signal
	sig := <-sigChan
	log.Info("received signal, shutting down", "signal", sig)
	cancel() // This tells workers to stop
	
	// Wait for workers to finish current jobs
	pool.Wait()
	metricsSrv.Shutdown(context.Background())

	log.Info("worker shutdown complete")
}

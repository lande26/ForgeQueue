package main

import (
	"context"
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
	"github.com/lande26/ForgeQueue/internal/reaper"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := config.Load()
	log := logger.Init("reaper")

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
	ownerID := fmt.Sprintf("reaper-%s-%d", hostname, os.Getpid())
	locker := lock.NewLock(rdb, ownerID, 30*time.Second)

	// Start reaper loop
	log.Info("reaper starting",
		"interval", cfg.ReaperInterval,
		"staleness_threshold", cfg.StalenessThreshold,
	)
	
	r := reaper.NewReaper(rdb, locker, cfg)
	
	var waitChan = make(chan struct{})
	go func() {
		r.Run(ctx)
		close(waitChan)
	}()

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
	select {
	case sig := <-sigChan:
		log.Info("received signal, shutting down", "signal", sig)
		cancel()
		metricsSrv.Shutdown(context.Background())
		<-waitChan // wait for reaper loop to exit cleanly
	case <-waitChan:
		// reaper exited on its own (shouldn't happen unless ctx cancelled)
	}

	log.Info("reaper shutdown complete")
}

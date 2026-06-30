package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lande26/ForgeQueue/internal/api"
	"github.com/lande26/ForgeQueue/internal/config"
	"github.com/lande26/ForgeQueue/internal/logger"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := config.Load()
	log := logger.Init("api")

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

	q := queue.NewQueue(rdb)
	handler := api.NewHandler(q, rdb)
	mux := api.NewRouter(handler)

	srv := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: mux,
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Info("received signal, shutting down", "signal", sig)
		cancel()
		
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Info("api server starting", "port", cfg.HTTPPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

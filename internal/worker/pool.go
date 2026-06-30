package worker

import (
	"context"
	"log/slog"
	"sync"

	"github.com/lande26/ForgeQueue/internal/config"
	"github.com/lande26/ForgeQueue/internal/lock"
	"github.com/lande26/ForgeQueue/internal/queue"
	"github.com/redis/go-redis/v9"
)

// Pool manages a group of concurrent workers.
type Pool struct {
	rdb      *redis.Client
	q        *queue.Queue
	locker   *lock.Lock
	registry Registry
	cfg      *config.Config
	workers  int
	wg       sync.WaitGroup
	logger   *slog.Logger
}

func NewPool(rdb *redis.Client, q *queue.Queue, locker *lock.Lock, registry Registry, cfg *config.Config) *Pool {
	return &Pool{
		rdb:      rdb,
		q:        q,
		locker:   locker,
		registry: registry,
		cfg:      cfg,
		workers:  cfg.WorkerConcurrency,
		logger:   slog.With("component", "pool"),
	}
}

// Start launches all workers in the pool in separate goroutines.
func (p *Pool) Start(ctx context.Context) {
	p.logger.Info("starting worker pool", "concurrency", p.workers)
	
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		
		go func(id int) {
			defer p.wg.Done()
			
			w := NewWorker(id, p.rdb, p.q, p.locker, p.registry, p.cfg)
			w.Run(ctx)
		}(i)
	}
}

// Wait blocks until all workers in the pool have shut down cleanly.
func (p *Pool) Wait() {
	p.logger.Info("waiting for workers to stop")
	p.wg.Wait()
	p.logger.Info("all workers stopped")
}

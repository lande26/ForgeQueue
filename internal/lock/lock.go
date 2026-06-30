package lock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lande26/ForgeQueue/internal/metrics"
	"github.com/redis/go-redis/v9"
)

var (
	ErrLockHeld    = errors.New("lock is held by another owner")
	ErrLockExpired = errors.New("lock expired before release")
)

// Lock provides distributed mutual exclusion backed by Redis.
type Lock struct {
	rdb     *redis.Client
	ownerID string
	ttl     time.Duration
}

// AcquiredLock represents a successfully acquired lock with its fencing token.
type AcquiredLock struct {
	Name         string
	FencingToken uint64
	lock         *Lock
	cancelRenew  context.CancelFunc
}

// NewLock creates a new distributed lock manager.
// ownerID should be unique per process (e.g., hostname-pid or UUID).
// ttl is the lease duration — the lock auto-expires after this if not renewed.
func NewLock(rdb *redis.Client, ownerID string, ttl time.Duration) *Lock {
	return &Lock{
		rdb:     rdb,
		ownerID: ownerID,
		ttl:     ttl,
	}
}

// Acquire attempts to acquire a named lock.
// Returns an AcquiredLock with a fencing token on success.
// The lock is automatically renewed in the background every TTL/3.
func (l *Lock) Acquire(ctx context.Context, name string) (*AcquiredLock, error) {
	lockKey := fmt.Sprintf("forgequeue:lock:%s", name)
	fencingKey := fmt.Sprintf("forgequeue:fencing:%s", name)

	// SET lockKey ownerID NX PX ttl
	ok, err := l.rdb.SetNX(ctx, lockKey, l.ownerID, l.ttl).Result()
	if err != nil {
		metrics.LockAcquisitions.WithLabelValues(name, "error").Inc()
		return nil, fmt.Errorf("lock acquire error: %w", err)
	}

	if !ok {
		metrics.LockAcquisitions.WithLabelValues(name, "rejected").Inc()
		return nil, ErrLockHeld
	}

	// Lock acquired — get a fencing token
	token, err := l.rdb.Incr(ctx, fencingKey).Result()
	if err != nil {
		// Lock was acquired but fencing token failed — release and bail
		releaseScript.Run(ctx, l.rdb, []string{lockKey}, l.ownerID)
		metrics.LockAcquisitions.WithLabelValues(name, "error").Inc()
		return nil, fmt.Errorf("fencing token error: %w", err)
	}

	metrics.LockAcquisitions.WithLabelValues(name, "acquired").Inc()

	// Start background renewal goroutine
	renewCtx, cancelRenew := context.WithCancel(ctx)
	acquired := &AcquiredLock{
		Name:         name,
		FencingToken: uint64(token),
		lock:         l,
		cancelRenew:  cancelRenew,
	}

	go l.renewLoop(renewCtx, lockKey, name)

	slog.Debug("lock acquired",
		"lock", name,
		"owner", l.ownerID,
		"token", token,
		"ttl", l.ttl,
	)

	return acquired, nil
}

// renewLoop runs in the background, renewing the lock every TTL/3.
func (l *Lock) renewLoop(ctx context.Context, lockKey string, name string) {
	interval := l.ttl / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := renewScript.Run(ctx, l.rdb, []string{lockKey}, l.ownerID, l.ttl.Milliseconds()).Int()
			if err != nil || result == 0 {
				slog.Warn("lock renewal failed, lock may have expired",
					"lock", name,
					"owner", l.ownerID,
					"error", err,
				)
				return
			}
			slog.Debug("lock renewed", "lock", name)
		}
	}
}

// Release releases an acquired lock.
// Stops the background renewal goroutine and conditionally deletes the lock
// only if this owner still holds it.
func (al *AcquiredLock) Release(ctx context.Context) error {
	// Stop renewal goroutine
	al.cancelRenew()

	lockKey := fmt.Sprintf("forgequeue:lock:%s", al.Name)

	result, err := releaseScript.Run(ctx, al.lock.rdb, []string{lockKey}, al.lock.ownerID).Int()
	if err != nil {
		return fmt.Errorf("lock release error: %w", err)
	}

	if result == 0 {
		slog.Warn("lock release: lock was not held by us (expired or stolen)",
			"lock", al.Name,
			"owner", al.lock.ownerID,
		)
		return ErrLockExpired
	}

	slog.Debug("lock released", "lock", al.Name, "owner", al.lock.ownerID)
	return nil
}

// Token returns the fencing token for this lock acquisition.
func (al *AcquiredLock) Token() uint64 {
	return al.FencingToken
}

# ForgeQueue: Taking the Job Queue Distributed with Redis

In the [previous post](<!-- link to asynq blog -->), we built an async job queue from scratch in Go — channels as queues, goroutine workers, retry logic with exponential backoff and jitter, a global job tracker, context-based timeouts, graceful shutdown, and a persistence layer with the factory pattern for rehydrating jobs. All of it ran in a single process. Go channels were the queue. `sync.WaitGroup` was the coordinator. Everything lived in one binary.

That works until it doesn't.

What happens when one machine isn't enough? When you want 10 workers across 5 containers? When the process crashes and all your in-memory jobs evaporate despite the JSON persister, because the new process doesn't know which jobs were mid-execution? When two instances of your app both try to process the same job?

That's where ForgeQueue starts. Same core ideas — submit, execute, retry, track — but now the queue lives in Redis, the workers are separate processes, and every single assumption about coordination changes.

I'm building this on top of the Redis knowledge from [Valkyr](<!-- link to Valkyr blog -->), where I wrote a Redis-compatible server from scratch — RESP2 parser, data stores, TTL engine, AOF persistence, the whole thing. That gave me a solid understanding of how Redis commands actually work internally. ForgeQueue is the flip side: using those commands to build something distributed on top of Redis, and discovering all the failure modes that come with it.

---

## What Changes When You Go Distributed

In our in-memory queue, the Go channel *was* the queue. Sending a job into the channel and a worker receiving it — that's an atomic handoff guaranteed by the Go runtime. No two goroutines receive the same message. The channel handles backpressure. `sync.WaitGroup` tells us when everything's done.

None of that applies anymore.

Redis replaces the channel. But Redis is a separate process, talking over TCP. Every interaction is a network call that can fail, timeout, or arrive out of order. Multiple worker processes poll the same Redis list independently. There is no `sync.WaitGroup` spanning processes on different machines.

Here's what we lose and have to rebuild:

| In-memory (asynq) | Distributed (ForgeQueue) |
|:---|:---|
| `chan *TrackedJob` | Redis List (`LPUSH` / `LMOVE`) |
| Channel receive = atomic handoff | Need `LMOVE` for atomic dequeue |
| Goroutine crash = still in-process | Process crash = job stuck in processing |
| `sync.WaitGroup` | Heartbeats + Reaper process |
| Mutex for thread safety | Lua scripts for atomicity |
| In-process retry loop | Retry state persisted in Redis hashes |
| Single process = no coordination | Distributed locks for mutual exclusion |

Basically every mechanism we built needs a distributed equivalent. Let's go through them.

---

## Redis Lists as the Queue

In the in-memory version, our queue was:

```go
jobs: make(chan *TrackedJob, buffer)
```

In ForgeQueue, it's a Redis list:

```
LPUSH queue:pending <job_id>
```

Producer pushes job IDs to the left. Workers pop from the right. FIFO ordering, same as a buffered channel.

For blocking behavior (equivalent to a goroutine blocking on `<-q.jobs`), Redis gives us `BRPOP` — the connection hangs until an element appears or a timeout expires. No tight polling loop, no wasted CPU. Conceptually identical to a goroutine waiting on a channel receive.

But here's the first difference from channels: **`BRPOP` deletes the element the moment it returns it**. In our in-memory version, if a goroutine panicked after receiving from the channel, we still had the `TrackedJob` in memory — the `WaitGroup` would hang, but the job wasn't *gone*. With Redis, `BRPOP` removes the job from the list. If the worker process crashes after receiving it, that job is just gone. No retry. No trace.

This is the single biggest difference between an in-process queue and a distributed one, and it's what forces us into a completely different dequeue pattern.

---

## Atomic Dequeue: LMOVE Replaces Channel Receive

The fix is to never fully remove a job until processing is confirmed. Instead of popping into the void, we atomically move the job from one list to another:

```
LMOVE queue:pending queue:processing RIGHT LEFT
```

One command. Atomic. The job leaves `pending` and appears in `processing` in the same operation. If the worker crashes after this, the job isn't lost — it's sitting in `processing` where something can find it later.

This is the distributed equivalent of our in-memory "running" status. In the asynq version, we tracked status with:

```go
t.Status = StatusRunning
```

Here, the job physically moving from the `pending` list to the `processing` list *is* the status transition. The list it lives in tells you its state.

(Side note: older Redis code uses `BRPOPLPUSH` for this. It was deprecated in Redis 7.0 in favor of `LMOVE` and its blocking variant `BLMOVE`. Same pattern, cleaner API.)

The job lifecycle becomes:

```
1. Job ID sits in "queue:pending"
2. Worker does LMOVE pending → processing (atomic)
3. Worker does the actual work
4. Worker removes the job from "queue:processing" (LREM)
5. Worker deletes job metadata
```

If the worker dies between step 2 and step 4, the job is in `processing` and stays there. In the in-memory version, our `defer` and `WaitGroup` handled this. Here, we need a completely separate process to detect and recover stuck jobs.

---

## The Reaper: Because There's No WaitGroup Across Processes

In the asynq version, if a goroutine panicked, the `WaitGroup` counter never decremented, and `Wait()` would block forever — a problem, but at least detectable. We also had `context.WithTimeout` propagating cancellation down to workers.

In a distributed system, there's no shared `WaitGroup`. Worker processes are independent OS processes, possibly on different machines. When one crashes, the others don't know. Redis doesn't know. The job just sits in the `processing` list forever.

Enter the Reaper — a separate process whose only job is to find stuck work and push it back to `pending`. It runs on a timer, scans the `processing` list, and checks each job's timestamps.

The naive approach:

```
1. LRANGE queue:processing 0 -1    (read all jobs in processing)
2. For each job, load metadata, check timestamps
3. If stale, LREM from processing, LPUSH back to pending
```

### The Race Condition We Never Had In-Memory

This has a bug that simply doesn't exist in the single-process version.

Worker is processing job X. It's slow but making progress. The Reaper scans, sees job X has been in `processing` for too long, decides to requeue. But between the Reaper reading the list and actually executing the requeue, the worker finishes job X — removes it from `processing`, cleans up, done. Now the Reaper pushes a *completed* job back to `pending`. Another worker picks it up. The job runs twice.

In the in-memory version, our mutex prevented this:

```go
t.mu.Lock()
t.Status = StatusSuccess
t.mu.Unlock()
```

One goroutine at a time could read or write the status. But Redis doesn't have mutexes. Multiple processes are issuing commands independently, and the interleaving between those commands is unpredictable.

### Lua Scripts: The Distributed Mutex

Redis executes Lua scripts atomically — the entire script runs as a single operation, no interleaving with other commands. This is the Redis equivalent of wrapping code in `t.mu.Lock()` / `t.mu.Unlock()`.

```lua
local heartbeat = tonumber(redis.call('HGET', KEYS[1], 'heartbeat_at'))
if not heartbeat then
    return 0
end

local threshold = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local job_id = ARGV[3]

if (now - heartbeat) > threshold then
    local removed = redis.call('LREM', KEYS[2], 1, job_id)
    if removed > 0 then
        redis.call('LPUSH', KEYS[3], job_id)
        local retries = tonumber(redis.call('HGET', KEYS[1], 'retry_count')) or 0
        redis.call('HSET', KEYS[1], 'retry_count', retries + 1)
        redis.call('HSET', KEYS[1], 'heartbeat_at', now)
        return 1
    end
end
return 0
```

The critical guard: `LREM` returning 0. If the worker already removed the job from `processing`, `LREM` finds nothing, the script returns 0, and no requeue happens. The check-and-move is indivisible. Race closed.

In our asynq codebase, we had `sync.Mutex` protecting `TrackedJob` fields and `sync.RWMutex` protecting the `JobTracker` map. Lua scripts serve the exact same purpose — they're just the distributed version of a mutex.

---

## Heartbeats: Solving a Problem That Doesn't Exist In-Memory

In the asynq version, we had `context.WithTimeout` for every job attempt:

```go
attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
err := t.Job.Execute(attemptCtx)
cancel()
```

If a job hung, the context deadline would fire, the goroutine would return an error, and the retry loop would handle it. Clean. Deterministic.

In the distributed version, there's no shared context between the Reaper and the worker. The Reaper can't cancel a worker's context — they're different processes. All the Reaper can do is look at timestamps and guess whether the worker is alive.

Early on, I had a single `updated_at` field for this. The Reaper checks: if `now - updated_at > threshold`, the job is stale.

The problem is subtle. `updated_at` serves double duty — it gets bumped on status transitions *and* acts as a liveness signal. Consider a job that legitimately takes 10 minutes. The worker updates metadata at minute 1 (marking it as "in progress"), then does heavy computation for 9 minutes. At minute 6, the Reaper sees a 5-minute-old timestamp and declares the job dead. The worker is fine. The Reaper just killed a healthy job.

In the in-memory version, this wasn't a problem because `context.WithTimeout` was *per-attempt* and deterministic. The timeout was the timeout. Here, the Reaper is guessing from the outside.

The fix is two separate fields:

- **`updated_at`** — changes on state transitions and metadata writes. For auditing.
- **`heartbeat_at`** — updated by a background goroutine in the worker, purely to signal "I'm alive."

```go
func (w *Worker) heartbeat(ctx context.Context, jobID string) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.redis.HSet(ctx, "job:"+jobID, "heartbeat_at", time.Now().Unix())
        }
    }
}
```

This is a goroutine pattern we already know from asynq — a background ticker running alongside the main work. The difference is it's writing to Redis instead of updating an in-memory struct, and it exists specifically because there's no `context.WithTimeout` spanning process boundaries.

---

## Idempotency Keys: A Problem That Simply Doesn't Exist In-Memory

In the asynq version, `Submit()` was a function call:

```go
q.Submit(&job.SleepyJob{Duration: 1 * time.Second}, job.SubmitOptions{...})
```

Function calls don't fail silently. They either execute or they don't. There's no situation where `Submit` succeeds but the caller doesn't know about it.

In ForgeQueue, job submission happens over HTTP. A client sends `POST /jobs`. The server writes the job to Redis. Starts sending back a 201 response. The network drops the response. The client sees a timeout. Did the job get created? No way to know. So the client retries. Now there are two identical jobs in the queue.

This entire class of problem — duplicate submissions due to network unreliability — doesn't exist when your queue is a Go channel in the same process.

The fix is idempotency keys:

```json
{
  "idempotency_key": "welcome-email-user-456",
  "type": "email",
  "payload": { "to": "user@example.com" }
}
```

Server side:

```go
created, err := rdb.SetNX(ctx, "idempotency:"+key, jobID, 24*time.Hour).Result()
if !created {
    existingID, _ := rdb.Get(ctx, "idempotency:"+key).Result()
    return existingID, nil
}
```

`SetNX` — set if not exists. Atomic. If the key already exists, the job was enqueued on the first attempt. The 24-hour TTL prevents idempotency keys from piling up forever.

---

## Redis Streams: Why I Didn't Use Them

Redis has a data structure called Streams (`XADD`, `XREADGROUP`, `XACK`) that's purpose-built for this kind of consumer-group pattern. It gives you an append-only log with consumer groups, message acknowledgment, and a pending entries list — which is essentially the `processing` queue I'm building manually with Lists.

```
XADD events * type "user.signup" user_id "456"
XREADGROUP GROUP workers worker-1 COUNT 1 BLOCK 5000 STREAMS events >
XACK events workers <message-id>
```

`XCLAIM` even handles stuck message recovery (the Reaper's job, basically).

Could ForgeQueue be built on Streams? Absolutely. But I went with Lists for two reasons. First, Lists give me explicit control over every state transition — I know where each job is because I put it there. With Streams, the pending entry management is implicit, and the edge cases around `XCLAIM` and `XAUTOCLAIM` get murky when you're layering retry logic, dead letter queues, and priority scheduling on top.

Second — and this is the real reason — building the reliability layer myself is the entire point. In the asynq blog, we could've used an existing queue library. We didn't because we wanted to understand the internals. Same principle applies here.

That said, the distinction between job queues and event streams matters and is worth understanding:

**Job queues** say "do this." A command. Destructive consumption — once processed, deleted. Single consumer per job. Retry-aware. This is ForgeQueue, Sidekiq, BullMQ, Celery.

**Event streams** say "this happened." A fact. Non-destructive — events stay in the log. Multiple consumer groups read independently. Replayable. This is Kafka, NATS JetStream, Redis Streams.

They solve fundamentally different problems. Picking the wrong one creates issues no amount of engineering fixes.

---

## Distributed Locking: Coordination Without sync.Mutex

In the asynq codebase, coordination was straightforward:

```go
// TrackedJob
t.mu.Lock()
t.Status = StatusRunning
t.mu.Unlock()

// JobTracker
jt.mu.RLock()
defer jt.mu.RUnlock()
```

`sync.Mutex` and `sync.RWMutex`. Clean, fast, guaranteed by the Go runtime.

In ForgeQueue, we have multiple worker processes and potentially multiple Reaper instances. Two workers shouldn't process the same job. Two Reapers shouldn't sweep at the same time. There is no shared mutex across separate processes.

I briefly considered pulling in an external distributed lock service built on Raft consensus — strongly consistent, battle-tested. But it would mean deploying and operating a separate cluster alongside Redis. Since Redis itself provides the primitives we need, I built the locking directly into ForgeQueue.

### SET NX PX: The Lock Primitive

```
SET lock:reaper-sweep worker-1 NX PX 30000
```

- **NX** — only set if the key doesn't exist. Atomic acquire.
- **PX 30000** — auto-expire after 30 seconds. The lease.

Returns OK if you got the lock. Returns nil if someone else holds it. If your process crashes, the lock releases itself after 30 seconds. No manual cleanup, no permanently held locks.

This is the distributed equivalent of `sync.Mutex.Lock()` — except it has a built-in timeout so dead processes don't hold locks forever.

### The Stale Lock Problem

`SET NX PX` has a gap that `sync.Mutex` doesn't:

1. Worker A acquires the lock.
2. Worker A hits a long GC pause or just runs longer than the 30-second lease.
3. The lock expires.
4. Worker B acquires the lock. Starts working.
5. Worker A wakes up. *Thinks* it still holds the lock.
6. Both write to the database.

With `sync.Mutex`, this can't happen — `Unlock()` is explicit, and the lock doesn't expire. But in a distributed system, the lease *must* expire (otherwise a crashed process holds the lock forever), which opens this window.

### Fencing Tokens

The fix is fencing tokens — a monotonically increasing counter. Every lock acquisition increments a global counter and returns the value:

```go
func (l *Lock) Acquire(ctx context.Context, name string) (uint64, error) {
    ok, err := l.rdb.SetNX(ctx, "lock:"+name, l.ownerID, l.ttl).Result()
    if !ok {
        return 0, ErrLockHeld
    }
    token, err := l.rdb.Incr(ctx, "fencing:"+name).Result()
    return uint64(token), err
}
```

When writing to an external system, the token travels with the write:

```sql
UPDATE results
SET data = $1, fencing_token = $2
WHERE job_id = $3 AND fencing_token < $2
```

Worker A's stale token is 5. Worker B's fresh token is 6. When Worker A finally writes with token 5, the database rejects it — 5 is not greater than the stored 6. The invariant holds even though the lock itself expired.

There's no equivalent to this problem in-memory because `sync.Mutex` doesn't have leases or expiration.

### Safe Release

One more thing that `sync.Mutex` handles automatically but we have to handle manually: releasing a lock you no longer own.

`sync.Mutex.Unlock()` just works — you either hold it or you don't. But with Redis, if you naively `DEL lock:reaper-sweep`, you might delete a lock that *someone else* acquired after yours expired. So release must be conditional — only delete if you're still the owner:

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
```

Lua again. The `GET` and `DEL` must be atomic. Same pattern as the Reaper script — check-then-act, indivisible.

---

## ForgeQueue's Architecture

Putting it all together, ForgeQueue has three separate processes. In the asynq version, all of this lived in `main.go` — the queue, the workers, the tracker, the persister. Here, they're independent binaries that only share Redis.

**Producer** — Accepts job requests over HTTP. Checks idempotency keys via `SetNX`. Writes job metadata to a Redis hash (`HSET job:{id} type email payload {...} heartbeat_at <now>`). Pushes the job ID to the `queue:pending` list.

**Worker** — Calls `BLMOVE` to atomically move a job from `pending` to `processing`. Acquires a distributed lock on the job ID. Spawns a heartbeat goroutine (ticker writing `heartbeat_at` to Redis every few seconds). Executes the job with retry logic — same exponential backoff with jitter we built in asynq, just with state persisted in Redis hashes instead of in-memory structs. On success: `LREM` from processing, clean up metadata, release lock. On failure after max retries: move to dead letter queue.

**Reaper** — Runs on a timer. Acquires `lock:reaper-sweep` so only one instance runs at a time. Scans `queue:processing` via `LRANGE`. For each job, loads metadata and checks `heartbeat_at`. Executes the Lua requeue script for stale jobs. Releases the lock.

All three connect to the same Redis instance. Redis is simultaneously the queue, the metadata store, the lock manager, and the coordination layer.

---

## What Going Distributed Actually Taught Me

Building asynq taught me the mechanics of a job queue — how workers pull from a queue, how retries work, how to track state, how to persist and rehydrate jobs. All valuable. All insufficient for the distributed case.

The jump from in-process to distributed isn't adding more features. It's dealing with a completely different failure model. In-process, things either work or they panic — and panics are deterministic, debuggable, local. Distributed, things fail *partially*. A write goes through but the ack doesn't come back. A process is alive but paused. A lock is held but the holder is dead. Every interaction with Redis is a network call that might fail in ways that leave the system in an ambiguous state.

The patterns that handle this aren't complicated — `LMOVE` for atomic dequeue, Lua for multi-step atomicity, heartbeats for liveness, idempotency keys for dedup, fencing tokens for stale locks, leases for automatic cleanup. They're all well-documented. But understanding *why* each one exists — what specific failure mode it prevents, what breaks when you skip it — that only clicked by building the system without them and watching things go wrong.

If you went through the asynq build, you already understand workers, retries, backoff, context cancellation, persistence. ForgeQueue takes every single one of those concepts and asks: "okay, but what if this was two processes on different machines talking over a network that drops packets?" The answer to that question is, roughly, this entire blog post.

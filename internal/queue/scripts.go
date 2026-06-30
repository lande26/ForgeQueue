package queue

import "github.com/redis/go-redis/v9"

// ReaperRequeueScript handles the atomic check-and-move for stalled jobs.
// KEYS[1] = forgequeue:job:{jobID} (hash)
// KEYS[2] = forgequeue:processing (list)
// KEYS[3] = forgequeue:pending (list)
// KEYS[4] = forgequeue:dead (list)
// ARGV[1] = staleness threshold in seconds
// ARGV[2] = current unix timestamp
// ARGV[3] = job ID string
var ReaperRequeueScript = redis.NewScript(`
local heartbeat = tonumber(redis.call('HGET', KEYS[1], 'heartbeat_at'))
if not heartbeat then
    return 0  -- job metadata doesn't exist, skip
end

local threshold = tonumber(ARGV[1])
local now = tonumber(ARGV[2])

if (now - heartbeat) > threshold then
    local removed = redis.call('LREM', KEYS[2], 1, ARGV[3])
    if removed > 0 then
        -- Check retry budget
        local retries = tonumber(redis.call('HGET', KEYS[1], 'retry_count')) or 0
        local max_retries = tonumber(redis.call('HGET', KEYS[1], 'max_retries')) or 3

        if retries >= max_retries then
            -- Exhausted: move to dead letter queue
            redis.call('LPUSH', KEYS[4], ARGV[3])
            redis.call('HSET', KEYS[1], 'status', 'dead')
            redis.call('HSET', KEYS[1], 'updated_at', now)
            return 2  -- signal: moved to dead
        else
            -- Requeue for retry
            redis.call('LPUSH', KEYS[3], ARGV[3])
            redis.call('HSET', KEYS[1], 'retry_count', retries + 1)
            redis.call('HSET', KEYS[1], 'heartbeat_at', now)
            redis.call('HSET', KEYS[1], 'status', 'pending')
            redis.call('HSET', KEYS[1], 'updated_at', now)
            redis.call('HSET', KEYS[1], 'last_error', 'rescued by reaper: worker presumed dead')
            return 1  -- signal: requeued
        end
    end
end
return 0  -- no action taken
`)

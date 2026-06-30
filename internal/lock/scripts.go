package lock

import "github.com/redis/go-redis/v9"

// Lua script: release lock only if we still own it.
// KEYS[1] = lock key
// ARGV[1] = owner ID
var releaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`)

// Lua script: renew lock TTL only if we still own it.
// KEYS[1] = lock key
// ARGV[1] = owner ID
// ARGV[2] = TTL in milliseconds
var renewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	redis.call("PEXPIRE", KEYS[1], ARGV[2])
	return 1
else
	return 0
end
`)

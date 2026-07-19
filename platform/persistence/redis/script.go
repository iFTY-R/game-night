package redis

import goredis "github.com/redis/go-redis/v9"

// tokenBucketScript uses Redis TIME so refill and expiry decisions do not depend on API host clock synchronization.
var tokenBucketScript = goredis.NewScript(`
local redis_time = redis.call('TIME')
local now_us = tonumber(redis_time[1]) * 1000000 + tonumber(redis_time[2])
local capacity = tonumber(ARGV[1])
local refill_every_us = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])

local stored = redis.call('HMGET', KEYS[1], 'tokens', 'updated_us')
local tokens = tonumber(stored[1])
local updated_us = tonumber(stored[2])
if tokens == nil or updated_us == nil then
    tokens = capacity
    updated_us = now_us
elseif now_us < updated_us then
    updated_us = now_us
end

tokens = math.min(capacity, tokens + ((now_us - updated_us) / refill_every_us))
local allowed = 0
local retry_ms = 0
if tokens >= 1 then
    tokens = tokens - 1
    allowed = 1
else
    retry_ms = math.max(1, math.ceil(((1 - tokens) * refill_every_us) / 1000))
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated_us', now_us)
redis.call('PEXPIRE', KEYS[1], ttl_ms)
return {allowed, retry_ms}
`)

-- ARGV: productId, warehouseId, delta. Returns the new qty, or "insufficient" (nothing changed).
local key = 'stock:' .. ARGV[1] .. ':' .. ARGV[2]
local new = tonumber(redis.call('GET', key) or '0') + tonumber(ARGV[3])
if new < 0 then return 'insufficient' end
redis.call('SET', key, new)
redis.call('SADD', 'warehouses:' .. ARGV[1], ARGV[2])
return tostring(new)

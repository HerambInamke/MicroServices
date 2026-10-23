-- ARGV: orderId. Puts the reserved stock back; "released" or "none" when there is nothing to release.
local rkey = 'reservation:' .. ARGV[1]
local raw = redis.call('GET', rkey)
if not raw then return 'none' end
local taken = cjson.decode(raw)
for i = 1, #taken, 2 do redis.call('INCRBY', taken[i], taken[i + 1]) end
redis.call('DEL', rkey)
return 'released'

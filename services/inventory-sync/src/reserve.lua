-- ARGV: orderId, then productId, qty pairs. Takes stock from warehouses in id order, all or nothing.
-- Products with no warehouses are untracked and always pass. Returns "ok" or "short:<productId>".
local rkey = 'reservation:' .. ARGV[1]
if redis.call('EXISTS', rkey) == 1 then return 'ok' end -- a retried reserve counts once
local taken = {}
for i = 2, #ARGV, 2 do
  local pid, need = ARGV[i], tonumber(ARGV[i + 1])
  local whs = redis.call('SMEMBERS', 'warehouses:' .. pid)
  if #whs > 0 then
    table.sort(whs)
    for _, w in ipairs(whs) do
      if need == 0 then break end
      local key = 'stock:' .. pid .. ':' .. w
      local take = math.min(tonumber(redis.call('GET', key) or '0'), need)
      if take > 0 then
        redis.call('DECRBY', key, take)
        table.insert(taken, key)
        table.insert(taken, take)
        need = need - take
      end
    end
    if need > 0 then
      for j = 1, #taken, 2 do redis.call('INCRBY', taken[j], taken[j + 1]) end
      return 'short:' .. pid
    end
  end
end
redis.call('SET', rkey, cjson.encode(taken))
return 'ok'

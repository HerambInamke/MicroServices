// Integration tests against real MongoDB (TEST_MONGO_URL, a throwaway database) and Redis (TEST_REDIS_URL).
const { test, before, after } = require('node:test');
const assert = require('node:assert');
const http = require('http');
const mongoose = require('mongoose');
const Redis = require('ioredis');
const WebSocket = require('ws');
const { createApp } = require('./app');
const { Thread, Message, Outbox, RelayLease } = require('./models');
const { outbox: realOutbox, redisBroadcaster, relayOnce } = require('./events');
const { chatSockets } = require('./ws');

const ROLES = { w1: 'wholesaler', w2: 'wholesaler', r1: 'retailer', r2: 'retailer' };
const users = {
  down: false,
  async get(id) {
    if (this.down) throw new Error('auth down');
    return ROLES[id] ? { id, role: ROLES[id] } : null;
  },
};
// The real outbox writer, with a switch to make the event write fail inside the transaction.
const outbox = {
  fail: false,
  async add(session, ...args) {
    if (this.fail) throw new Error('outbox write failed');
    return realOutbox.add(session, ...args);
  },
};

// Committed outbox events, oldest first, as {topic, type, key, payload, eventId}.
async function outboxEvents() {
  const rows = await Outbox.find().sort({ _id: 1 });
  return rows.map((r) => {
    const e = JSON.parse(r.event);
    return { topic: r.topic, type: e.eventType, key: r.key, payload: e.payload, eventId: e.eventId };
  });
}

let server, base, redisPub, redisSub;

before(async () => {
  await mongoose.connect(process.env.TEST_MONGO_URL);
  await mongoose.connection.dropDatabase();
  await Promise.all([Thread.syncIndexes(), Message.syncIndexes(), Outbox.createCollection(), RelayLease.createCollection()]);
  redisPub = new Redis(process.env.TEST_REDIS_URL);
  redisSub = new Redis(process.env.TEST_REDIS_URL);
  server = http.createServer(createApp({ users, outbox, broadcast: redisBroadcaster(redisPub) }));
  server.on('upgrade', chatSockets(redisSub));
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `127.0.0.1:${server.address().port}`;
});

after(async () => {
  server.close();
  redisPub.disconnect();
  redisSub.disconnect();
  await mongoose.connection.dropDatabase();
  await mongoose.disconnect();
});

async function call(method, path, user, body) {
  const headers = { 'content-type': 'application/json' };
  if (user) Object.assign(headers, { 'x-user-id': user, 'x-user-role': ROLES[user] || 'admin' });
  const r = await fetch(`http://${base}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  return { status: r.status, body: await r.json() };
}

async function thread(a, b) {
  const r = await call('POST', '/chat/threads', a, { participantId: b });
  assert.ok([200, 201].includes(r.status), JSON.stringify(r.body));
  return r.body.data.id;
}

test('threads are idempotent per pair and need one wholesaler and one retailer', async () => {
  let r = await call('POST', '/chat/threads', 'r1', { participantId: 'w1' });
  assert.equal(r.status, 201);
  assert.deepEqual([r.body.data.wholesalerId, r.body.data.retailerId], ['w1', 'r1']);
  const id = r.body.data.id;

  r = await call('POST', '/chat/threads', 'w1', { participantId: 'r1' });
  assert.equal(r.status, 200);
  assert.equal(r.body.data.id, id); // same pair from the other side → same thread

  assert.equal((await call('POST', '/chat/threads', 'r1', { participantId: 'r2' })).status, 400);
  assert.equal((await call('POST', '/chat/threads', 'r1', { participantId: 'r1' })).status, 400);
  assert.equal((await call('POST', '/chat/threads', 'r1', { participantId: 'ghost' })).status, 404);
  assert.equal((await call('POST', '/chat/threads', 'admin1', { participantId: 'w1' })).status, 403);
  assert.equal((await call('POST', '/chat/threads', null, { participantId: 'w1' })).status, 401);
  users.down = true;
  r = await call('POST', '/chat/threads', 'r2', { participantId: 'w2' });
  users.down = false;
  assert.equal(r.status, 502);
});

test('messages: send publishes one event, only participants read, paging works', async () => {
  const id = await thread('r1', 'w1');
  await Outbox.deleteMany({});

  let r = await call('POST', `/chat/threads/${id}/messages`, 'r1', { text: '  hello  ' });
  assert.equal(r.status, 201);
  assert.equal(r.body.data.text, 'hello');
  const events = await outboxEvents();
  assert.equal(events.length, 1);
  assert.equal(events[0].topic, 'chat.message.sent');
  assert.deepEqual(events[0].payload, { threadId: id, messageId: r.body.data.id, senderId: 'r1', recipientId: 'w1', kind: 'text' });

  for (const text of ['', ' ', 'x'.repeat(2001)]) {
    assert.equal((await call('POST', `/chat/threads/${id}/messages`, 'r1', { text })).status, 400);
  }
  assert.equal((await call('POST', `/chat/threads/${id}/messages`, 'r2', { text: 'hi' })).status, 403);
  assert.equal((await call('GET', `/chat/threads/${id}/messages`, 'w2')).status, 403);
  assert.equal((await call('GET', '/chat/threads/not-an-id/messages', 'w1')).status, 404);

  await call('POST', `/chat/threads/${id}/messages`, 'w1', { text: 'second' });
  await call('POST', `/chat/threads/${id}/messages`, 'r1', { text: 'third' });
  r = await call('GET', `/chat/threads/${id}/messages?limit=2`, 'w1');
  assert.deepEqual(r.body.data.map((m) => m.text), ['third', 'second']);
  assert.equal(r.body.meta.hasMore, true);
  r = await call('GET', `/chat/threads/${id}/messages?limit=2&before=${r.body.data[1].id}`, 'w1');
  assert.deepEqual(r.body.data.map((m) => m.text), ['hello']);
  assert.equal(r.body.meta.hasMore, false);

  r = await call('GET', '/chat/threads', 'r1');
  assert.equal(r.body.data[0].id, id); // newest activity first
  assert.equal((await call('GET', '/chat/threads', 'r2')).body.data.length, 0);
});

test('offers: only the other party responds, once; counters chain', async () => {
  const id = await thread('r2', 'w2');
  await Outbox.deleteMany({});
  const base = `/chat/threads/${id}/offers`;

  for (const bad of [{ qty: 0, unitPrice: 10 }, { qty: 1.5, unitPrice: 10 }, { qty: 2, unitPrice: 10.123 }, { qty: 2, unitPrice: -1 }, { qty: 2 }]) {
    assert.equal((await call('POST', base, 'r2', { productId: 'p1', ...bad })).status, 400, JSON.stringify(bad));
  }
  assert.equal((await call('POST', base, 'r2', { qty: 2, unitPrice: 10 })).status, 400);

  let r = await call('POST', base, 'r2', { productId: 'p1', qty: 10, unitPrice: 1200.5 });
  assert.equal(r.status, 201);
  const first = r.body.data;
  assert.deepEqual(first.offer, { productId: 'p1', qty: 10, unitPrice: 1200.5, status: 'open', respondsTo: null });

  assert.equal((await call('POST', `${base}/${first.id}/respond`, 'r2', { action: 'accept' })).status, 403); // own offer
  assert.equal((await call('POST', `${base}/${first.id}/respond`, 'w1', { action: 'accept' })).status, 403); // outsider
  assert.equal((await call('POST', `${base}/${first.id}/respond`, 'w2', { action: 'haggle' })).status, 400);
  assert.equal((await call('POST', `${base}/${first.id}/respond`, 'w2', { action: 'counter', qty: 10 })).status, 400);

  r = await call('POST', `${base}/${first.id}/respond`, 'w2', { action: 'counter', qty: 10, unitPrice: 1300 });
  assert.equal(r.status, 201);
  const counter = r.body.data;
  assert.deepEqual(counter.offer, { productId: 'p1', qty: 10, unitPrice: 1300, status: 'open', respondsTo: first.id });
  assert.equal(counter.senderId, 'w2');

  r = await call('POST', `${base}/${first.id}/respond`, 'w2', { action: 'accept' });
  assert.equal(r.status, 409);
  assert.equal(r.body.error.code, 'OFFER_NOT_OPEN');

  // Two concurrent accepts: exactly one wins.
  const both = await Promise.all([1, 2].map(() => call('POST', `${base}/${counter.id}/respond`, 'r2', { action: 'accept' })));
  assert.deepEqual(both.map((x) => x.status).sort(), [200, 409]);

  const msgs = (await call('GET', `/chat/threads/${id}/messages`, 'r2')).body.data;
  assert.deepEqual(msgs.map((m) => m.offer.status), ['accepted', 'countered']);
  // offer → w2, counter → r2, accept → the counter's sender (w2) via chat.offer.updated
  const events = await outboxEvents();
  assert.deepEqual(events.map((e) => [e.topic, e.payload.recipientId]), [['chat.message.sent', 'w2'], ['chat.message.sent', 'r2'], ['chat.offer.updated', 'w2']]);
  assert.equal(events[2].payload.status, 'accepted');
});

test('internal offer read: returns the offer with the thread parties, refuses identity headers', async () => {
  const id = await thread('r1', 'w2');
  const offer = (await call('POST', `/chat/threads/${id}/offers`, 'w2', { productId: '7', qty: 5, unitPrice: 99.5 })).body.data;
  await call('POST', `/chat/threads/${id}/offers/${offer.id}/respond`, 'r1', { action: 'accept' });

  let r = await call('GET', `/chat/offers/${offer.id}`);
  assert.equal(r.status, 200);
  assert.deepEqual(r.body.data, { id: offer.id, productId: '7', qty: 5, unitPrice: 99.5, status: 'accepted', respondsTo: null, wholesalerId: 'w2', retailerId: 'r1' });

  assert.equal((await call('GET', `/chat/offers/${offer.id}`, 'r1')).status, 403);
  const text = (await call('POST', `/chat/threads/${id}/messages`, 'r1', { text: 'hi' })).body.data;
  for (const bad of [text.id, 'nope', new mongoose.Types.ObjectId().toString()]) {
    r = await call('GET', `/chat/offers/${bad}`);
    assert.equal(r.status, 404, bad);
    assert.equal(r.body.error.code, 'OFFER_NOT_FOUND');
  }
});

test('a counter that fails to post leaves the original offer open', async () => {
  const id = await thread('r1', 'w1');
  const offer = (await call('POST', `/chat/threads/${id}/offers`, 'r1', { productId: 'p9', qty: 1, unitPrice: 10 })).body.data;
  const before = (await outboxEvents()).length;
  const messages = await Message.countDocuments({ threadId: id });
  outbox.fail = true;
  const r = await call('POST', `/chat/threads/${id}/offers/${offer.id}/respond`, 'w1', { action: 'counter', qty: 1, unitPrice: 12 });
  outbox.fail = false;
  assert.equal(r.status, 500);
  // The whole response rolled back: no counter message, no event, original still open.
  assert.equal(await Message.countDocuments({ threadId: id }), messages);
  assert.equal((await outboxEvents()).length, before);
  assert.equal((await call('POST', `/chat/threads/${id}/offers/${offer.id}/respond`, 'w1', { action: 'accept' })).status, 200);
});

test('a message whose event cannot be written is not saved', async () => {
  const id = await thread('r2', 'w1');
  outbox.fail = true;
  const r = await call('POST', `/chat/threads/${id}/messages`, 'r2', { text: 'lost?' });
  outbox.fail = false;
  assert.equal(r.status, 500);
  assert.equal(await Message.countDocuments({ threadId: id }), 0);
});

test('relay: keeps events while Kafka is down, then sends them in order once', async () => {
  await Outbox.deleteMany({});
  await RelayLease.deleteMany({});
  const id = await thread('r1', 'w2');
  for (const text of ['one', 'two']) await call('POST', `/chat/threads/${id}/messages`, 'r1', { text });
  const written = await outboxEvents();

  const down = async () => {
    throw new Error('kafka unavailable');
  };
  assert.equal(await relayOnce(down), 0);
  assert.equal((await outboxEvents()).length, 2);

  const tried = [];
  const firstFails = async (topic, key, value) => {
    tried.push(JSON.parse(value).eventId);
    if (tried.length === 1) throw new Error('timeout');
  };
  assert.equal(await relayOnce(firstFails), 0);
  assert.deepEqual(tried, [written[0].eventId]); // the second never overtakes the first

  const sent = [];
  assert.equal(await relayOnce(async (topic, key, value) => sent.push({ topic, key, eventId: JSON.parse(value).eventId })), 2);
  assert.deepEqual(sent, written.map((e) => ({ topic: 'chat.message.sent', key: id, eventId: e.eventId })));
  assert.equal((await outboxEvents()).length, 0);
});

test('relay: only the lease holder sends', async () => {
  await Outbox.deleteMany({});
  const id = await thread('r2', 'w2');
  await call('POST', `/chat/threads/${id}/messages`, 'r2', { text: 'hi' });
  await RelayLease.updateOne({ _id: 'outbox' }, { $set: { owner: 'another-instance', until: new Date(Date.now() + 60_000) } }, { upsert: true });
  assert.equal(await relayOnce(async () => {}), 0);
  await RelayLease.updateOne({ _id: 'outbox' }, { $set: { until: new Date(Date.now() - 1) } }); // that lease expired
  assert.equal(await relayOnce(async () => {}), 1);
});

function connect(threadId, user) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`ws://${base}/ws/chat/${threadId}`, { headers: user ? { 'x-user-id': user } : {} });
    ws.once('open', () => resolve(ws));
    ws.once('unexpected-response', (req, res) => reject(Object.assign(new Error('refused'), { status: res.statusCode })));
    ws.once('error', reject);
  });
}

test('websocket: participants get messages live, outsiders are refused', async () => {
  const id = await thread('r1', 'w2');
  await assert.rejects(connect(id, 'w1'), { status: 403 });
  await assert.rejects(connect(id, null), { status: 401 });
  await assert.rejects(connect(new mongoose.Types.ObjectId().toString(), 'w2'), { status: 404 });

  const ws = await connect(id, 'w2');
  const got = new Promise((resolve) => ws.once('message', (m) => resolve(JSON.parse(m))));
  await new Promise((r) => setTimeout(r, 100)); // let the Redis SUBSCRIBE land
  await call('POST', `/chat/threads/${id}/messages`, 'r1', { text: 'live?' });
  const event = await got;
  assert.equal(event.type, 'message');
  assert.equal(event.message.text, 'live?');
  ws.close();
});

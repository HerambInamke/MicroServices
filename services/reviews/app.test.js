// Integration tests against a real MongoDB (TEST_MONGO_URL should point at a throwaway database).
const { test, before, after } = require('node:test');
const assert = require('node:assert');
const http = require('http');
const mongoose = require('mongoose');
const { createApp } = require('./app');
const { Review, OrderOutcome } = require('./models');
const { handleStatusChanged } = require('./consumer');
const { trustScore } = require('./score');

// Fake order-payment: same party check as the real GET /orders/:id.
const ORDERS = {
  o1: { id: 'o1', retailerId: 'r1', wholesalerId: 'w1', status: 'delivered' },
  o2: { id: 'o2', retailerId: 'r1', wholesalerId: 'w1', status: 'shipped' },
  o3: { id: 'o3', retailerId: 'r2', wholesalerId: 'w1', status: 'delivered' },
};
const orders = {
  down: false,
  async get(id, caller) {
    if (this.down) throw new Error('connection refused');
    const order = ORDERS[id];
    if (!order) return { status: 404, order: null };
    if (![order.retailerId, order.wholesalerId].includes(caller.id)) return { status: 403, order: null };
    return { status: 200, order };
  },
};

let server, base;

before(async () => {
  await mongoose.connect(process.env.TEST_MONGO_URL);
  await mongoose.connection.dropDatabase();
  await Promise.all([Review.syncIndexes(), OrderOutcome.syncIndexes()]);
  server = http.createServer(createApp({ orders }));
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
});

after(async () => {
  server.close();
  await mongoose.connection.dropDatabase();
  await mongoose.disconnect();
});

async function call(method, path, user, body) {
  const headers = { 'content-type': 'application/json' };
  if (user) Object.assign(headers, { 'x-user-id': user, 'x-user-role': user[0] === 'w' ? 'wholesaler' : 'retailer' });
  const r = await fetch(base + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  return { status: r.status, body: await r.json() };
}

test('score formula pins', () => {
  assert.equal(trustScore({ reviewCount: 0, ratingSum: 0, delivered: 0, cancelled: 0 }), 50);
  assert.equal(trustScore({ reviewCount: 1, ratingSum: 5, delivered: 0, cancelled: 0 }), 56);
  assert.equal(trustScore({ reviewCount: 1000, ratingSum: 5000, delivered: 1000, cancelled: 0 }), 100);
});

test('submit: only the retailer of a delivered order, once', async () => {
  const cases = [
    [null, { orderId: 'o1', rating: 5 }, 401],
    ['r1', { orderId: 'o1', rating: 0 }, 400],
    ['r1', { orderId: 'o1', rating: 6 }, 400],
    ['r1', { orderId: 'o1', rating: 4.5 }, 400],
    ['r1', { orderId: 'o1', rating: 5, text: 'x'.repeat(2001) }, 400],
    ['r1', { rating: 5 }, 400],
    ['r1', { orderId: 'nope', rating: 5 }, 404],
    ['r1', { orderId: 'o3', rating: 5 }, 403], // someone else's order
    ['w1', { orderId: 'o1', rating: 5 }, 403], // the wholesaler cannot review their own sale
    ['r1', { orderId: 'o2', rating: 5 }, 409], // not delivered
  ];
  for (const [user, body, status] of cases) {
    const r = await call('POST', '/reviews', user, body);
    assert.equal(r.status, status, `${user} ${JSON.stringify(body)} → ${JSON.stringify(r.body)}`);
  }

  orders.down = true;
  assert.equal((await call('POST', '/reviews', 'r1', { orderId: 'o1', rating: 5 })).status, 502);
  orders.down = false;

  let r = await call('POST', '/reviews', 'r1', { orderId: 'o1', rating: 5, text: ' great ', wholesalerId: 'w-forged' });
  assert.equal(r.status, 201, JSON.stringify(r.body));
  assert.equal(r.body.data.wholesalerId, 'w1'); // from the order, not the body
  assert.equal(r.body.data.text, 'great');

  r = await call('POST', '/reviews', 'r1', { orderId: 'o1', rating: 1 });
  assert.equal(r.status, 409);
  assert.equal(r.body.error.code, 'ALREADY_REVIEWED');

  r = await call('GET', '/reviews?wholesalerId=w1');
  assert.equal(r.status, 200);
  assert.equal(r.body.meta.total, 1);
  assert.equal(r.body.data[0].retailerId, undefined); // public list hides the buyer
  assert.equal(r.body.data[0].orderId, undefined);
  assert.equal((await call('GET', '/reviews')).status, 400);
});

const event = (orderId, status, wholesalerId = 'w9') => JSON.stringify({ eventId: 'e', payload: { orderId, wholesalerId, status } });

test('trust score moves with reviews and fulfilment; redelivery counts once', async () => {
  let r = await call('GET', '/trust-score/w9');
  assert.deepEqual(r.body.data, { wholesalerId: 'w9', score: 50, reviewCount: 0, avgRating: null, delivered: 0, cancelled: 0 });

  await handleStatusChanged(event('a', 'delivered'));
  await handleStatusChanged(event('a', 'delivered')); // redelivered
  await handleStatusChanged(event('b', 'shipped')); // not an outcome
  await handleStatusChanged('not json');
  await handleStatusChanged(JSON.stringify({ payload: { status: 'delivered' } }));
  r = await call('GET', '/trust-score/w9');
  assert.equal(r.body.data.delivered, 1);
  const afterDelivery = r.body.data.score;
  assert.ok(afterDelivery > 50);

  await handleStatusChanged(event('c', 'cancelled'));
  await handleStatusChanged(event('d', 'cancelled'));
  r = await call('GET', '/trust-score/w9');
  assert.equal(r.body.data.cancelled, 2);
  assert.ok(r.body.data.score < afterDelivery);

  r = await call('GET', '/trust-score/w1'); // one 5-star review from the previous test
  assert.equal(r.body.data.score, 56);
  assert.equal(r.body.data.avgRating, 5);
});

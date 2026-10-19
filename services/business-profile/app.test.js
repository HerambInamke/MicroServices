// Integration tests against a real MongoDB (MONGO_URL should point at a throwaway database).
const { test, before, after } = require('node:test');
const assert = require('node:assert');
const http = require('http');
const mongoose = require('mongoose');
const { createApp } = require('./app');
const { Profile } = require('./models');

let server, base;

before(async () => {
  await mongoose.connect(process.env.MONGO_URL);
  await mongoose.connection.dropDatabase();
  await Profile.syncIndexes();
  server = http.createServer(createApp());
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
});

after(async () => {
  server.close();
  await mongoose.connection.dropDatabase();
  await mongoose.disconnect();
});

async function call(method, path, { user, role = 'wholesaler', body } = {}) {
  const headers = { 'content-type': 'application/json' };
  if (user) Object.assign(headers, { 'x-user-id': user, 'x-user-role': role });
  const r = await fetch(base + path, { method, headers, body: body === undefined ? undefined : typeof body === 'string' ? body : JSON.stringify(body) });
  return { status: r.status, body: await r.json() };
}

const GST = '27AAPFU0939F1ZV';

test('create, read and duplicate profile', async () => {
  let r = await call('POST', '/profiles', { body: { businessName: 'Anon' } });
  assert.equal(r.status, 401);

  r = await call('POST', '/profiles', { user: 'u1', body: { businessName: 'Kanchi Silks', gstNumber: GST.toLowerCase(), userId: 'hijack' } });
  assert.equal(r.status, 201, JSON.stringify(r.body));
  assert.equal(r.body.data.userId, 'u1'); // userId comes from identity, not the body
  assert.equal(r.body.data.businessType, 'wholesaler'); // defaulted from role
  assert.equal(r.body.data.gstNumber, GST); // normalised to uppercase

  r = await call('POST', '/profiles', { user: 'u1', body: { businessName: 'Again' } });
  assert.equal(r.status, 409);
  assert.equal(r.body.error.code, 'PROFILE_EXISTS');

  r = await call('GET', '/profiles/u1');
  assert.equal(r.status, 200);
  assert.equal(r.body.data.businessName, 'Kanchi Silks');

  r = await call('GET', '/profiles/nobody');
  assert.equal(r.status, 404);
});

test('validation errors', async () => {
  const cases = [
    { businessName: 'X', gstNumber: 'NOTAGST' },
    { gstNumber: GST },
    { businessName: 'X', businessType: 'pirate' },
  ];
  for (const body of cases) {
    const r = await call('POST', '/profiles', { user: 'u-bad', role: 'retailer', body });
    assert.equal(r.status, 400, JSON.stringify(body));
    assert.equal(r.body.error.code, 'VALIDATION_ERROR');
  }
  const r = await call('POST', '/profiles', { user: 'u-bad', body: '{not json' });
  assert.equal(r.status, 400);
  assert.equal(r.body.error.code, 'INVALID_BODY');
});

test('only the owner can update or add certifications', async () => {
  await call('POST', '/profiles', { user: 'u2', role: 'retailer', body: { businessName: 'Retail Co' } });

  let r = await call('PATCH', '/profiles/u2', { user: 'u3', body: { businessName: 'Stolen' } });
  assert.equal(r.status, 403);
  r = await call('POST', '/profiles/u2/certifications', { user: 'u3', body: { name: 'Fake' } });
  assert.equal(r.status, 403);

  r = await call('PATCH', '/profiles/u2', { user: 'u2', body: { businessName: 'Retail Co Ltd', gstNumber: 'bad' } });
  assert.equal(r.status, 400); // validators run on update

  r = await call('PATCH', '/profiles/u2', { user: 'u2', body: { businessName: 'Retail Co Ltd', userId: 'u9' } });
  assert.equal(r.status, 200);
  assert.equal(r.body.data.businessName, 'Retail Co Ltd');
  assert.equal(r.body.data.userId, 'u2');

  r = await call('POST', '/profiles/u2/certifications', { user: 'u2', body: { name: 'Silk Mark', issuer: 'Silk Mark Org' } });
  assert.equal(r.status, 201);
  assert.equal(r.body.data.certifications.length, 1);
  assert.equal(r.body.data.certifications[0].name, 'Silk Mark');

  r = await call('POST', '/profiles/u2/certifications', { user: 'u2', body: {} });
  assert.equal(r.status, 400);
});

test('list with filter, search and paging', async () => {
  for (let i = 0; i < 3; i++) {
    await call('POST', '/profiles', { user: `w${i}`, body: { businessName: `Banaras Weaves ${i}` } });
  }
  let r = await call('GET', '/profiles?businessType=wholesaler&q=banaras&limit=2');
  assert.equal(r.status, 200);
  assert.equal(r.body.data.length, 2);
  assert.equal(r.body.meta.total, 3);
  assert.equal(r.body.meta.hasMore, true);

  r = await call('GET', '/profiles?q=banaras&page=2&limit=2');
  assert.equal(r.body.data.length, 1);
  assert.equal(r.body.meta.hasMore, false);

  // Regex metacharacters are matched literally, not executed.
  r = await call('GET', '/profiles?q=.*');
  assert.equal(r.body.meta.total, 0);

  r = await call('GET', '/profiles?limit=1000');
  assert.equal(r.status, 400);
});

// End-to-end smoke test against the running stack: `docker compose up -d`, then `node scripts/smoke.mjs`.
// Drives the Phase 1 flow through the API gateway only, as a real client would.
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { Wallet, getBytes } from 'ethers';

const BASE = process.env.GATEWAY_URL || 'http://localhost:3000/api/v1';
const ADMIN = { email: process.env.ADMIN_EMAIL || 'admin@provenance.local', password: process.env.ADMIN_PASSWORD || 'admin-dev-password' };
const run = Date.now().toString(36);

async function api(method, path, { token, body, form } = {}) {
  const headers = {};
  if (token) headers.authorization = `Bearer ${token}`;
  if (body !== undefined) headers['content-type'] = 'application/json';
  const res = await fetch(BASE + path, { method, headers, body: form ?? (body === undefined ? undefined : JSON.stringify(body)) });
  const json = await res.json();
  return { status: res.status, ...json };
}

function step(name) {
  console.log(`✓ ${name}`);
}

async function signupAndLogin(role, label = role) {
  const email = `${label}-${run}@smoke.test`;
  const s = await api('POST', '/auth/signup', { body: { email, password: 'password123', role } });
  assert.equal(s.status, 201, JSON.stringify(s));
  const l = await api('POST', '/auth/login', { body: { email, password: 'password123' } });
  assert.equal(l.status, 200, JSON.stringify(l));
  return { id: s.data.id, token: l.data.accessToken };
}

// --- Checkpoint A: identity ---
const health = await api('GET', '/health');
assert.equal(health.status, 200, JSON.stringify(health));
step('all services healthy');

const wholesaler = await signupAndLogin('wholesaler');
const retailer = await signupAndLogin('retailer');
const me = await api('GET', '/auth/me', { token: wholesaler.token });
assert.equal(me.data.role, 'wholesaler');
assert.equal((await api('GET', '/auth/me')).status, 401);
step('signup, login and /me (anonymous rejected)');

const wallet = `0x${randomBytes(20).toString('hex')}`;
const link = await api('POST', '/auth/wallet/link', { token: wholesaler.token, body: { address: wallet } });
assert.equal(link.status, 200, JSON.stringify(link));
step('wallet linked');

const admin = await api('POST', '/auth/login', { body: ADMIN });
assert.equal(admin.status, 200, 'admin seed login failed');
const adminToken = admin.data.accessToken;

// --- Checkpoint B: a wholesaler can list ---
const profile = await api('POST', '/profiles', { token: wholesaler.token, body: { businessName: `Smoke Silks ${run}`, gstNumber: '27AAPFU0939F1ZV' } });
assert.equal(profile.status, 201, JSON.stringify(profile));
assert.equal((await api('GET', `/profiles/${wholesaler.id}`)).data.businessName, `Smoke Silks ${run}`);
step('business profile created and publicly readable');

const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, ...new Array(64).fill(0)]);
const form = new FormData();
form.append('file', new Blob([png], { type: 'image/png' }), 'saree.png');
const media = await api('POST', '/media/upload', { token: wholesaler.token, form });
assert.equal(media.status, 201, JSON.stringify(media));
const content = await fetch(BASE.replace('/api/v1', '') + media.data.url);
assert.equal(content.status, 200);
assert.equal(Buffer.compare(Buffer.from(await content.arrayBuffer()), Buffer.from(png)), 0);
step('image uploaded to MinIO and served back');

const category = await api('POST', '/categories', { token: adminToken, body: { name: `Sarees ${run}` } });
assert.equal(category.status, 201, JSON.stringify(category));
assert.equal((await api('POST', '/categories', { token: wholesaler.token, body: { name: 'Nope' } })).status, 403);
step('admin created category (wholesaler forbidden)');

const product = await api('POST', '/products', {
  token: wholesaler.token,
  body: { name: 'Kanjeevaram Silk Saree', description: 'Pure mulberry silk', categoryId: category.data.id, moq: 10, basePrice: 2500 },
});
assert.equal(product.status, 201, JSON.stringify(product));
assert.equal((await api('POST', '/products', { token: retailer.token, body: { name: 'x', categoryId: category.data.id, moq: 1, basePrice: 1 } })).status, 403);
const tiers = await api('POST', `/products/${product.data.id}/price-tiers`, {
  token: wholesaler.token,
  body: { tiers: [{ minQty: 50, unitPrice: 2200 }, { minQty: 100, unitPrice: 2000 }] },
});
assert.equal(tiers.status, 200, JSON.stringify(tiers));
const listed = await api('GET', `/products?wholesalerId=${wholesaler.id}`);
assert.equal(listed.meta.total, 1);
step('product listed with bulk price tiers (retailer forbidden)');

// --- Checkpoint C: a retailer can browse, order and pay ---
const browse = await api('GET', `/products/${product.data.id}`);
assert.equal(browse.status, 200);
step('retailer browses the product anonymously');

const below = await api('POST', '/orders', { token: retailer.token, body: { items: [{ productId: product.data.id, qty: 5 }] } });
assert.equal(below.status, 422, JSON.stringify(below));
assert.equal(below.error.code, 'BELOW_MOQ');
assert.equal((await api('POST', '/orders', { token: wholesaler.token, body: { items: [{ productId: product.data.id, qty: 60 }] } })).status, 403);
step('order below MOQ rejected; wholesaler cannot place orders');

const order = await api('POST', '/orders', { token: retailer.token, body: { items: [{ productId: product.data.id, qty: 60 }] } });
assert.equal(order.status, 201, JSON.stringify(order));
assert.equal(order.data.total, 60 * 2200); // tier price for 50+ units, computed by the catalog
assert.equal(order.data.wholesalerId, wholesaler.id);
step(`bulk order placed at tier price (total ₹${order.data.total})`);

const seen = await api('GET', '/orders', { token: wholesaler.token });
assert.ok(seen.data.some((o) => o.id === order.data.id));
const stranger = await signupAndLogin('retailer', 'stranger');
assert.equal((await api('GET', `/orders/${order.data.id}`, { token: stranger.token })).status, 403);
step('wholesaler sees the order; other retailers cannot');

const confirmed = await api('PATCH', `/orders/${order.data.id}/status`, { token: wholesaler.token, body: { status: 'confirmed' } });
assert.equal(confirmed.status, 200, JSON.stringify(confirmed));
assert.equal((await api('PATCH', `/orders/${order.data.id}/status`, { token: wholesaler.token, body: { status: 'delivered' } })).status, 409);
step('wholesaler confirmed the order (illegal transition rejected)');

const payment = await api('POST', `/orders/${order.data.id}/payment`, { token: retailer.token });
if (payment.status === 503) {
  assert.equal(payment.error.code, 'PAYMENTS_NOT_CONFIGURED');
  step('payment endpoint reachable (Razorpay keys not set, so it reports 503 by design)');
} else {
  assert.equal(payment.status, 200, JSON.stringify(payment));
  assert.equal(payment.data.amount, 60 * 2200 * 100);
  step(`Razorpay order created: ${payment.data.razorpayOrderId}`);
}

console.log(`\nAll Phase 1 smoke checks passed. Order id: ${order.data.id}`);

// --- Checkpoint D: provenance ledger ---
// Each actor links a fresh wallet and signs like MetaMask's personal_sign over the offered dataHash.
async function actor(role) {
  const user = await signupAndLogin(role, `${role}-p2`);
  const wallet = Wallet.createRandom();
  const linked = await api('POST', '/auth/wallet/link', { token: user.token, body: { address: wallet.address } });
  assert.equal(linked.status, 200, JSON.stringify(linked));
  return { ...user, wallet };
}

const weaver = await actor('weaver');
const dyeHouse = await actor('dye-house');
const seller = await actor('wholesaler');
const shop = await actor('retailer');

assert.equal((await api('POST', '/pieces', { body: {} })).status, 401);
const piece = await api('POST', '/pieces', { token: weaver.token, body: { productId: product.data.id, description: 'Smoke-test Banarasi saree' } });
assert.equal(piece.status, 201, JSON.stringify(piece));
const pieceId = piece.data.id;
step(`weaver registered piece ${pieceId}`);

// The weaver prints the tag at the loom: one code for the piece's whole life.
const tag = await api('POST', `/qr/${pieceId}/generate`, { token: weaver.token });
assert.equal(tag.status, 201, JSON.stringify(tag));
assert.ok(Buffer.from(tag.data.pngBase64, 'base64').subarray(0, 4).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47])));
assert.equal((await api('POST', `/qr/${pieceId}/generate`, { token: dyeHouse.token })).status, 403);
step(`weaver generated QR code ${tag.data.code} (non-owner refused)`);

async function handoff(from, to) {
  const offer = await api('POST', `/pieces/${pieceId}/handoffs`, { token: from.token, body: { toUserId: to.id } });
  assert.equal(offer.status, 201, JSON.stringify(offer));
  const signature = await to.wallet.signMessage(getBytes(offer.data.pendingHandoff.dataHash));
  const accepted = await api('POST', `/pieces/${pieceId}/handoffs/accept`, { token: to.token, body: { signature } });
  assert.equal(accepted.status, 200, JSON.stringify(accepted));
  assert.equal(accepted.data.actorAddress, to.wallet.address);
  return accepted.data;
}

const first = await handoff(weaver, dyeHouse);
step(`dye-house signed receipt on-chain (tx ${first.txHash})`);

// Negative paths on the next offer: a stranger cannot accept, a forged signature is rejected.
const offerToSeller = await api('POST', `/pieces/${pieceId}/handoffs`, { token: dyeHouse.token, body: { toUserId: seller.id } });
assert.equal(offerToSeller.status, 201, JSON.stringify(offerToSeller));
const forged = await Wallet.createRandom().signMessage(getBytes(offerToSeller.data.pendingHandoff.dataHash));
assert.equal((await api('POST', `/pieces/${pieceId}/handoffs/accept`, { token: shop.token, body: { signature: forged } })).status, 403);
assert.equal((await api('POST', `/pieces/${pieceId}/handoffs/accept`, { token: seller.token, body: { signature: forged } })).status, 403);
step('stranger and forged-signature accepts rejected');

await handoff(dyeHouse, seller);
await handoff(seller, shop);
step('wholesaler and retailer signed their handoffs');

const history = await api('GET', `/pieces/${pieceId}/history`);
assert.equal(history.status, 200);
assert.deepEqual(history.data.events.map((e) => e.role), ['weaver', 'dye-house', 'wholesaler', 'retailer']);
assert.equal(history.data.piece.currentOwnerId, shop.id);
const verified = await api('GET', `/pieces/${pieceId}/verify`);
assert.equal(verified.status, 200);
assert.equal(verified.data.verified, true, JSON.stringify(verified.data));
assert.equal(verified.data.onChainCount, 3);
step('public history shows 4 events and matches the contract');

// --- Checkpoint E: a consumer scans the tag ---
const scanned = await api('GET', `/qr/${tag.data.code}/resolve`);
assert.equal(scanned.status, 200, JSON.stringify(scanned));
assert.equal(scanned.data.pieceId, pieceId);
assert.equal(scanned.data.history.events.length, 4);
assert.equal((await api('POST', `/qr/${tag.data.code}/scan-log`)).status, 201);
assert.equal((await api('GET', '/qr/not-a-real-code/resolve')).status, 404);
step('anonymous scan resolves the full history; unknown code is 404');

const reissue = await api('POST', `/qr/${pieceId}/generate`, { token: shop.token });
assert.equal(reissue.status, 200, JSON.stringify(reissue));
assert.equal(reissue.data.code, tag.data.code);
assert.equal((await api('POST', `/qr/${pieceId}/generate`, { token: weaver.token })).status, 403);
step('new owner gets the same code; the weaver no longer can');

console.log(`\nAll Phase 2 smoke checks passed. Piece id: ${pieceId}, QR code: ${tag.data.code}`);

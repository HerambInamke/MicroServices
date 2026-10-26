// End-to-end smoke test against the running stack: `docker compose up -d`, then `node scripts/smoke.mjs`.
// Drives the Phase 1 flow through the API gateway only, as a real client would.
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import zlib from 'node:zlib';
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

// --- Checkpoint F: counterfeit detection ---
// Needs an empty design registry: there is no design delete, so a re-run's image conflicts with the last run's.
// Reset it with `docker compose up -d --force-recreate weaviate counterfeit-detection`.
// Real decodable PNGs so CLIP can embed them: a red/gold checked pattern and an unrelated blue gradient.
function pngOf(size, pixel) {
  const raw = Buffer.alloc(size * (size * 3 + 1));
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) raw.set(pixel(x, y), y * (size * 3 + 1) + 1 + x * 3);
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(zlib.crc32(Buffer.concat([Buffer.from(type), data])));
    return Buffer.concat([len, Buffer.from(type), data, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 2, 0, 0, 0], 8); // 8-bit RGB
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', ihdr), chunk('IDAT', zlib.deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}
const sareeDesign = pngOf(224, (x, y) => ((x >> 4) + (y >> 4)) % 2 ? [180, 20, 40] : [220, 170, 40]);
const unrelated = pngOf(224, (x, y) => [0, x, 255 - y]);
const pngForm = (bytes) => {
  const f = new FormData();
  f.append('file', new Blob([bytes], { type: 'image/png' }), 'design.png');
  return f;
};

const designMedia = await api('POST', '/media/upload', { token: wholesaler.token, form: pngForm(sareeDesign) });
assert.equal(designMedia.status, 201, JSON.stringify(designMedia));
const design = await api('POST', '/designs', { token: wholesaler.token, body: { productId: product.data.id, mediaId: designMedia.data.id } });
assert.equal(design.status, 201, JSON.stringify(design));
assert.equal((await api('GET', `/designs/${design.data.id}`)).data.productId, product.data.id);
assert.equal((await api('POST', '/designs', { token: retailer.token, body: { productId: product.data.id, mediaId: designMedia.data.id } })).status, 403);
step(`wholesaler registered design ${design.data.id} (retailer refused, public read)`);

// shortcut: matches the identical image; switch to a transformed copy once the threshold is calibrated on real photos.
const hit = await api('POST', '/designs/match', { token: retailer.token, form: pngForm(sareeDesign) });
assert.equal(hit.status, 200, JSON.stringify(hit));
assert.equal(hit.data.match, true, JSON.stringify(hit.data));
assert.equal(hit.data.results[0].design.id, design.data.id);
const miss = await api('POST', '/designs/match', { token: retailer.token, form: pngForm(unrelated) });
assert.equal(miss.status, 200, JSON.stringify(miss));
assert.equal(miss.data.match, false, JSON.stringify(miss.data));
assert.equal((await api('POST', '/designs/match', { form: pngForm(sareeDesign) })).status, 401);
step(`photo matched its design (${hit.data.results[0].similarity}); unrelated photo did not (${miss.data.results[0]?.similarity})`);

const copier = await signupAndLogin('wholesaler', 'copier');
const copierProduct = await api('POST', '/products', {
  token: copier.token,
  body: { name: 'Copied Saree', categoryId: category.data.id, moq: 1, basePrice: 100 },
});
assert.equal(copierProduct.status, 201, JSON.stringify(copierProduct));
const copierMedia = await api('POST', '/media/upload', { token: copier.token, form: pngForm(sareeDesign) });
const copied = await api('POST', '/designs', { token: copier.token, body: { productId: copierProduct.data.id, mediaId: copierMedia.data.id } });
assert.equal(copied.status, 409, JSON.stringify(copied));
assert.equal(copied.error.code, 'DESIGN_CONFLICT');
step('another wholesaler registering the same image is refused (409)');

console.log(`\nAll Phase 3 smoke checks passed. Design id: ${design.data.id}`);

// --- Phase 4: search, inventory, analytics ---
// Search and analytics follow Kafka, so they are eventually consistent: poll until the change shows up.
async function eventually(what, check, timeoutMs = 60_000) {
  const until = Date.now() + timeoutMs;
  for (;;) {
    const value = await check();
    if (value) return value;
    if (Date.now() > until) assert.fail(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 1000));
  }
}

const paithaniName = `Paithani ${run}`;
const paithani = await api('POST', '/products', {
  token: wholesaler.token,
  body: { name: paithaniName, description: 'Handwoven silk', categoryId: category.data.id, moq: 1, basePrice: 1800 },
});
assert.equal(paithani.status, 201, JSON.stringify(paithani));
const found = await eventually('product in search', async () => {
  const r = await api('GET', `/search?q=${encodeURIComponent(paithaniName)}&type=product`);
  return r.data?.find((d) => d.id === String(paithani.data.id));
});
assert.equal(found.wholesalerId, wholesaler.id);
assert.ok((await api('GET', `/search/suggest?q=${encodeURIComponent(paithaniName.slice(0, 6))}`)).data.length > 0);
step(`new product searchable via product.created (${paithaniName})`);

assert.equal((await api('POST', '/search/reindex', { token: wholesaler.token })).status, 403);
const reindex = await api('POST', '/search/reindex', { token: adminToken });
assert.equal(reindex.status, 200, JSON.stringify(reindex));
assert.ok(reindex.data.products >= 2 && reindex.data.designs >= 1, JSON.stringify(reindex.data));
const designHit = await api('GET', `/search?q=${encodeURIComponent('Kanjeevaram')}&type=design`);
assert.ok(designHit.data.some((d) => d.id === design.data.id), JSON.stringify(designHit.data));
step(`admin reindex (${JSON.stringify(reindex.data)}); designs searchable by product name`);

const pid = paithani.data.id;
assert.equal((await api('PATCH', `/inventory/${pid}`, { token: retailer.token, body: { warehouseId: 'surat', delta: 10 } })).status, 403);
const stocked = await api('PATCH', `/inventory/${pid}`, { token: wholesaler.token, body: { warehouseId: 'surat', delta: 10 } });
assert.equal(stocked.status, 200, JSON.stringify(stocked));
assert.equal(stocked.data.total, 10);
const firstBuy = await api('POST', '/orders', { token: retailer.token, body: { items: [{ productId: pid, qty: 6 }] } });
assert.equal(firstBuy.status, 201, JSON.stringify(firstBuy));
const oversell = await api('POST', '/orders', { token: retailer.token, body: { items: [{ productId: pid, qty: 6 }] } });
assert.equal(oversell.status, 409, JSON.stringify(oversell));
assert.equal(oversell.error.code, 'OUT_OF_STOCK');
assert.equal((await api('GET', `/inventory/${pid}`)).data.total, 4);
assert.equal((await api('POST', '/inventory/reserve', { token: adminToken, body: { orderId: 'x', items: [{ productId: pid, qty: 1 }] } })).status, 404);
step('stock 10: order of 6 placed, second order of 6 refused (409); reserve not reachable from outside');

const cancelled = await api('PATCH', `/orders/${firstBuy.data.id}/status`, { token: wholesaler.token, body: { status: 'cancelled' } });
assert.equal(cancelled.status, 200, JSON.stringify(cancelled));
assert.equal((await api('GET', `/inventory/${pid}`)).data.total, 10);
step('cancelling the order released its stock');

assert.equal((await api('GET', `/analytics/seller/${wholesaler.id}/summary`, { token: retailer.token })).status, 403);
const summary = await eventually('seller summary', async () => {
  const r = await api('GET', `/analytics/seller/${wholesaler.id}/summary?period=7d`, { token: wholesaler.token });
  return r.data?.topProducts.some((p) => p.productId === pid) && r.data.ordersByStatus.cancelled >= 1 && r.data;
});
const trends = await api('GET', `/analytics/trends?category=${category.data.id}&period=30d`);
assert.equal(trends.status, 200, JSON.stringify(trends));
assert.ok(trends.data.weeks.some((w) => w.qty >= 6), JSON.stringify(trends.data));
step(`seller summary and public trends reflect the orders (${JSON.stringify(summary.ordersByStatus)})`);

console.log('\nAll Phase 4 smoke checks passed.');

// --- Phase 5: notifications, chat/negotiation, reviews ---
const wsBase = BASE.replace(/^http/, 'ws');
// Opens a WebSocket through the gateway and collects every JSON frame it receives.
async function live(path, token) {
  const ws = new WebSocket(`${wsBase}${path}?access_token=${token}`);
  const frames = [];
  ws.addEventListener('message', (e) => frames.push(JSON.parse(e.data)));
  await new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve, { once: true });
    ws.addEventListener('error', () => reject(new Error(`websocket ${path} refused`)), { once: true });
  });
  await new Promise((r) => setTimeout(r, 300)); // let the service subscribe before we produce anything
  return { ws, frames };
}

const inbox = await live('/ws/notifications', wholesaler.token);
const fifthOrder = await api('POST', '/orders', { token: retailer.token, body: { items: [{ productId: pid, qty: 1 }] } });
assert.equal(fifthOrder.status, 201, JSON.stringify(fifthOrder));
await eventually('order notification on the live socket', () => inbox.frames.find((n) => n.refId === fifthOrder.data.id));
const listed5 = await api('GET', '/notifications?unread=true', { token: wholesaler.token });
const orderNote = listed5.data.find((n) => n.refId === fifthOrder.data.id);
assert.ok(orderNote, JSON.stringify(listed5));
assert.equal((await api('PATCH', `/notifications/${orderNote.id}/read`, { token: retailer.token })).status, 404);
assert.equal((await api('PATCH', `/notifications/${orderNote.id}/read`, { token: wholesaler.token })).status, 200);
step('placing an order notified the wholesaler live and in the inbox; only they can mark it read');

const threadRes = await api('POST', '/chat/threads', { token: retailer.token, body: { participantId: wholesaler.id } });
assert.equal(threadRes.status, 201, JSON.stringify(threadRes));
const threadId = threadRes.data.id;
assert.equal((await api('POST', '/chat/threads', { token: wholesaler.token, body: { participantId: retailer.id } })).data.id, threadId);
assert.equal((await api('GET', `/chat/threads/${threadId}/messages`, { token: stranger.token })).status, 403);
const room = await live(`/ws/chat/${threadId}`, wholesaler.token);
await assert.rejects(live(`/ws/chat/${threadId}`, stranger.token));

const offer = await api('POST', `/chat/threads/${threadId}/offers`, { token: retailer.token, body: { productId: String(pid), qty: 50, unitPrice: 1650 } });
assert.equal(offer.status, 201, JSON.stringify(offer));
await eventually('offer on the wholesaler socket', () => room.frames.find((f) => f.message?.id === offer.data.id));
const counter = await api('POST', `/chat/threads/${threadId}/offers/${offer.data.id}/respond`, { token: wholesaler.token, body: { action: 'counter', qty: 50, unitPrice: 1720 } });
assert.equal(counter.status, 201, JSON.stringify(counter));
assert.equal((await api('POST', `/chat/threads/${threadId}/offers/${counter.data.id}/respond`, { token: wholesaler.token, body: { action: 'accept' } })).status, 403);
const accepted = await api('POST', `/chat/threads/${threadId}/offers/${counter.data.id}/respond`, { token: retailer.token, body: { action: 'accept' } });
assert.equal(accepted.status, 200, JSON.stringify(accepted));
assert.equal(accepted.data.offer.status, 'accepted');
const again = await api('POST', `/chat/threads/${threadId}/offers/${counter.data.id}/respond`, { token: retailer.token, body: { action: 'accept' } });
assert.equal(again.error?.code, 'OFFER_NOT_OPEN');
await eventually('offer notification', async () => (await api('GET', '/notifications', { token: wholesaler.token })).data.some((n) => n.refType === 'thread' && n.refId === threadId));
step('thread opened; offer → counter → accept seen live; double accept 409; offer notified');
room.ws.close();
inbox.ws.close();

const early = await api('POST', '/reviews', { token: retailer.token, body: { orderId: fifthOrder.data.id, rating: 5 } });
assert.equal(early.status, 409, JSON.stringify(early));
assert.equal(early.error.code, 'ORDER_NOT_DELIVERED');
assert.equal((await api('POST', '/reviews', { token: stranger.token, body: { orderId: fifthOrder.data.id, rating: 1 } })).status, 403);
const score = await api('GET', `/trust-score/${wholesaler.id}`);
assert.equal(score.status, 200, JSON.stringify(score));
assert.equal(score.data.reviewCount, 0);
assert.equal((await api('GET', `/reviews?wholesalerId=${wholesaler.id}`)).status, 200);
step(`undelivered order cannot be reviewed; public trust score ${score.data.score} (delivered → review needs a paid order, i.e. Razorpay keys)`);

console.log('\nAll Phase 5 smoke checks passed.');

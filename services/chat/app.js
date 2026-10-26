const express = require('express');
const mongoose = require('mongoose');
const { Thread, Message, isParticipant, otherParty, threadView, messageView } = require('./models');

const ROLES = ['wholesaler', 'retailer'];
const MAX_TEXT = 2000;
const MAX_QTY = 1_000_000;
const MAX_PRICE = 100_000_000;

const ok = (res, status, data, meta = {}) => res.status(status).json({ data, error: null, meta });
const fail = (res, status, code, message) => res.status(status).json({ data: null, error: { code, message }, meta: {} });

class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    Object.assign(this, { status, code });
  }
}

// Identity headers are set by the gateway after it verifies the JWT.
function requireUser(req, res, next) {
  const userId = req.get('x-user-id');
  if (!userId) return fail(res, 401, 'UNAUTHENTICATED', 'authentication required');
  req.user = { id: userId, role: req.get('x-user-role') || '' };
  next();
}

const wrap = (fn) => (req, res, next) => fn(req, res, next).catch(next);

const isPositiveInt = (v, max) => Number.isInteger(v) && v > 0 && v <= max;
// Two decimal places at most: the price in paise must be a whole number.
const isPrice = (v) => typeof v === 'number' && v > 0 && v <= MAX_PRICE && Math.abs(Math.round(v * 100) - v * 100) < 1e-6;

function parseLimit(raw, fallback) {
  if (raw === undefined) return fallback;
  const n = Number(raw);
  return Number.isInteger(n) && n >= 1 && n <= 100 ? n : null;
}

async function loadThread(threadId, userId) {
  if (!mongoose.isValidObjectId(threadId)) throw new ApiError(404, 'THREAD_NOT_FOUND', 'thread not found');
  const thread = await Thread.findById(threadId);
  if (!thread) throw new ApiError(404, 'THREAD_NOT_FOUND', 'thread not found');
  if (!isParticipant(thread, userId)) throw new ApiError(403, 'FORBIDDEN', 'you are not part of this thread');
  return thread;
}

function offerFields(body) {
  const { qty, unitPrice } = body || {};
  if (!isPositiveInt(qty, MAX_QTY)) throw new ApiError(400, 'VALIDATION_ERROR', `qty must be a whole number between 1 and ${MAX_QTY}`);
  if (!isPrice(unitPrice)) throw new ApiError(400, 'VALIDATION_ERROR', 'unitPrice must be a positive amount with at most 2 decimals');
  return { qty, unitPrice };
}

/**
 * users.get(id, caller) → {id, role} | null; publisher.publish(topic, type, key, payload); broadcast(threadId, event).
 */
function createApp({ users, publisher, broadcast }) {
  const app = express();
  app.disable('x-powered-by');
  app.use(express.json({ limit: '100kb' }));

  app.get('/health', (req, res) => {
    res.status(200).json({ status: 'ok', service: 'chat' });
  });

  // Saves a message, bumps the thread, then tells the live sockets and the notifications service.
  async function post(thread, senderId, fields) {
    const message = await Message.create({ threadId: thread._id, senderId, ...fields });
    await Thread.updateOne({ _id: thread._id }, { $set: { lastMessageAt: message.createdAt } });
    const view = messageView(message);
    broadcast(view.threadId, { type: 'message', message: view });
    await publisher.publish('chat.message.sent', 'ChatMessageSent', view.threadId, {
      threadId: view.threadId,
      messageId: view.id,
      senderId,
      recipientId: otherParty(thread, senderId),
      kind: view.kind,
    });
    return view;
  }

  app.post('/chat/threads', requireUser, wrap(async (req, res) => {
    const { participantId } = req.body || {};
    if (typeof participantId !== 'string' || !participantId || participantId.length > 200) {
      throw new ApiError(400, 'VALIDATION_ERROR', 'participantId is required');
    }
    if (participantId === req.user.id) throw new ApiError(400, 'VALIDATION_ERROR', 'you cannot start a thread with yourself');
    if (!ROLES.includes(req.user.role)) throw new ApiError(403, 'FORBIDDEN', 'only wholesalers and retailers can chat');

    let other;
    try {
      other = await users.get(participantId, req.user);
    } catch (err) {
      console.error('auth lookup failed:', err.message);
      throw new ApiError(502, 'UPSTREAM_UNAVAILABLE', 'could not look up the participant');
    }
    if (!other) throw new ApiError(404, 'USER_NOT_FOUND', 'participant not found');
    if (!ROLES.includes(other.role) || other.role === req.user.role) {
      throw new ApiError(400, 'INVALID_PARTICIPANTS', 'a thread is between one wholesaler and one retailer');
    }

    const pair = req.user.role === 'wholesaler'
      ? { wholesalerId: req.user.id, retailerId: participantId }
      : { wholesalerId: participantId, retailerId: req.user.id };
    let result;
    try {
      result = await Thread.findOneAndUpdate(pair, { $setOnInsert: { lastMessageAt: new Date() } }, { upsert: true, new: true, includeResultMetadata: true });
    } catch (err) {
      if (err.code !== 11000) throw err;
      result = { value: await Thread.findOne(pair), lastErrorObject: { updatedExisting: true } }; // lost a create race
    }
    ok(res, result.lastErrorObject.updatedExisting ? 200 : 201, threadView(result.value));
  }));

  app.get('/chat/threads', requireUser, wrap(async (req, res) => {
    const limit = parseLimit(req.query.limit, 50);
    if (!limit) throw new ApiError(400, 'INVALID_PAGING', 'limit must be between 1 and 100');
    const threads = await Thread.find({ $or: [{ wholesalerId: req.user.id }, { retailerId: req.user.id }] })
      .sort({ lastMessageAt: -1 })
      .limit(limit);
    ok(res, 200, threads.map(threadView));
  }));

  app.get('/chat/threads/:id/messages', requireUser, wrap(async (req, res) => {
    const thread = await loadThread(req.params.id, req.user.id);
    const limit = parseLimit(req.query.limit, 50);
    if (!limit) throw new ApiError(400, 'INVALID_PAGING', 'limit must be between 1 and 100');
    const filter = { threadId: thread._id };
    if (req.query.before !== undefined) {
      if (!mongoose.isValidObjectId(req.query.before)) throw new ApiError(400, 'INVALID_PAGING', 'before must be a message id');
      filter._id = { $lt: req.query.before };
    }
    // One extra row tells us whether an older page exists.
    const rows = await Message.find(filter).sort({ _id: -1 }).limit(limit + 1);
    ok(res, 200, rows.slice(0, limit).map(messageView), { limit, hasMore: rows.length > limit });
  }));

  app.post('/chat/threads/:id/messages', requireUser, wrap(async (req, res) => {
    const thread = await loadThread(req.params.id, req.user.id);
    const text = typeof req.body?.text === 'string' ? req.body.text.trim() : '';
    if (!text || text.length > MAX_TEXT) throw new ApiError(400, 'VALIDATION_ERROR', `text must be 1 to ${MAX_TEXT} characters`);
    ok(res, 201, await post(thread, req.user.id, { kind: 'text', text }));
  }));

  app.post('/chat/threads/:id/offers', requireUser, wrap(async (req, res) => {
    const thread = await loadThread(req.params.id, req.user.id);
    const { productId } = req.body || {};
    if (typeof productId !== 'string' || !productId || productId.length > 200) throw new ApiError(400, 'VALIDATION_ERROR', 'productId is required');
    const offer = { productId, ...offerFields(req.body), status: 'open' };
    ok(res, 201, await post(thread, req.user.id, { kind: 'offer', offer }));
  }));

  app.post('/chat/threads/:id/offers/:msgId/respond', requireUser, wrap(async (req, res) => {
    const thread = await loadThread(req.params.id, req.user.id);
    const action = req.body?.action;
    if (!['accept', 'decline', 'counter'].includes(action)) throw new ApiError(400, 'VALIDATION_ERROR', 'action must be accept, decline or counter');
    const counter = action === 'counter' ? offerFields(req.body) : null; // validate before changing anything

    if (!mongoose.isValidObjectId(req.params.msgId)) throw new ApiError(404, 'OFFER_NOT_FOUND', 'offer not found');
    const original = await Message.findOne({ _id: req.params.msgId, threadId: thread._id, kind: 'offer' });
    if (!original) throw new ApiError(404, 'OFFER_NOT_FOUND', 'offer not found');
    if (original.senderId === req.user.id) throw new ApiError(403, 'FORBIDDEN', 'you cannot respond to your own offer');

    const status = { accept: 'accepted', decline: 'declined', counter: 'countered' }[action];
    // Atomic on status 'open': of two concurrent responses only one wins.
    const updated = await Message.findOneAndUpdate({ _id: original._id, 'offer.status': 'open' }, { $set: { 'offer.status': status } }, { new: true });
    if (!updated) throw new ApiError(409, 'OFFER_NOT_OPEN', 'this offer has already been answered');
    const view = messageView(updated);
    broadcast(view.threadId, { type: 'offer.updated', message: view });

    if (!counter) return ok(res, 200, view);
    let next;
    try {
      next = await post(thread, req.user.id, {
        kind: 'offer',
        offer: { productId: original.offer.productId, ...counter, status: 'open', respondsTo: original._id },
      });
    } catch (err) {
      // Without the counter message the original must stay answerable.
      await Message.updateOne({ _id: original._id, 'offer.status': 'countered' }, { $set: { 'offer.status': 'open' } });
      throw err;
    }
    ok(res, 201, next);
  }));

  app.use((req, res) => fail(res, 404, 'NOT_FOUND', 'no such route'));

  // eslint-disable-next-line no-unused-vars
  app.use((err, req, res, next) => {
    if (err instanceof ApiError) return fail(res, err.status, err.code, err.message);
    if (err.type === 'entity.parse.failed') return fail(res, 400, 'INVALID_BODY', 'request body must be valid JSON');
    if (err instanceof mongoose.Error.ValidationError || err instanceof mongoose.Error.CastError) {
      return fail(res, 400, 'VALIDATION_ERROR', 'request has an invalid value');
    }
    console.error(err);
    fail(res, 500, 'INTERNAL', 'something went wrong');
  });

  return app;
}

module.exports = { createApp, loadThread };

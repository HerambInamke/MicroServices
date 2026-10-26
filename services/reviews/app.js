const express = require('express');
const mongoose = require('mongoose');
const { Review, OrderOutcome, reviewView, publicReviewView } = require('./models');
const { trustScore } = require('./score');

const MAX_TEXT = 2000;

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

function parsePaging(query) {
  const page = query.page === undefined ? 1 : Number(query.page);
  const limit = query.limit === undefined ? 20 : Number(query.limit);
  if (!Number.isInteger(page) || page < 1 || !Number.isInteger(limit) || limit < 1 || limit > 100) return null;
  return { page, limit };
}

/** orders.get(orderId, caller) → {status, order}: the order-payment HTTP status and, on 200, the order. */
function createApp({ orders }) {
  const app = express();
  app.disable('x-powered-by');
  app.use(express.json({ limit: '100kb' }));

  app.get('/health', (req, res) => {
    res.status(200).json({ status: 'ok', service: 'reviews' });
  });

  app.post('/reviews', requireUser, wrap(async (req, res) => {
    const { orderId, rating } = req.body || {};
    const text = req.body?.text === undefined ? '' : req.body.text;
    if (typeof orderId !== 'string' || !orderId || orderId.length > 100) throw new ApiError(400, 'VALIDATION_ERROR', 'orderId is required');
    if (!Number.isInteger(rating) || rating < 1 || rating > 5) throw new ApiError(400, 'VALIDATION_ERROR', 'rating must be a whole number from 1 to 5');
    if (typeof text !== 'string' || text.length > MAX_TEXT) throw new ApiError(400, 'VALIDATION_ERROR', `text must be at most ${MAX_TEXT} characters`);

    // The order is read with the caller's identity; wholesaler and status come from it, never from the client.
    let found;
    try {
      found = await orders.get(orderId, req.user);
    } catch (err) {
      console.error('order lookup failed:', err.message);
      throw new ApiError(502, 'UPSTREAM_UNAVAILABLE', 'could not look up the order');
    }
    if (found.status === 404) throw new ApiError(404, 'ORDER_NOT_FOUND', 'order not found');
    if (found.status === 403) throw new ApiError(403, 'FORBIDDEN', 'you can only review your own orders');
    if (found.status !== 200) throw new ApiError(502, 'UPSTREAM_UNAVAILABLE', 'could not look up the order');
    const order = found.order;
    if (order.retailerId !== req.user.id) throw new ApiError(403, 'FORBIDDEN', 'you can only review your own orders');
    if (order.status !== 'delivered') throw new ApiError(409, 'ORDER_NOT_DELIVERED', 'only delivered orders can be reviewed');

    try {
      const review = await Review.create({ orderId: order.id, wholesalerId: order.wholesalerId, retailerId: req.user.id, rating, text: text.trim() });
      ok(res, 201, reviewView(review));
    } catch (err) {
      if (err.code === 11000) throw new ApiError(409, 'ALREADY_REVIEWED', 'this order has already been reviewed');
      throw err;
    }
  }));

  app.get('/reviews', wrap(async (req, res) => {
    const { wholesalerId } = req.query;
    if (typeof wholesalerId !== 'string' || !wholesalerId) throw new ApiError(400, 'VALIDATION_ERROR', 'wholesalerId is required');
    const paging = parsePaging(req.query);
    if (!paging) throw new ApiError(400, 'INVALID_PAGING', 'page must be >= 1 and limit between 1 and 100');
    const [items, total] = await Promise.all([
      Review.find({ wholesalerId }).sort({ createdAt: -1, _id: -1 }).skip((paging.page - 1) * paging.limit).limit(paging.limit),
      Review.countDocuments({ wholesalerId }),
    ]);
    ok(res, 200, items.map(publicReviewView), { ...paging, total, hasMore: paging.page * paging.limit < total });
  }));

  app.get('/trust-score/:wholesalerId', wrap(async (req, res) => {
    const { wholesalerId } = req.params;
    const [[ratings], delivered, cancelled] = await Promise.all([
      Review.aggregate([{ $match: { wholesalerId } }, { $group: { _id: null, n: { $sum: 1 }, sum: { $sum: '$rating' } } }]),
      OrderOutcome.countDocuments({ wholesalerId, status: 'delivered' }),
      OrderOutcome.countDocuments({ wholesalerId, status: 'cancelled' }),
    ]);
    const reviewCount = ratings ? ratings.n : 0;
    const ratingSum = ratings ? ratings.sum : 0;
    ok(res, 200, {
      wholesalerId,
      score: trustScore({ reviewCount, ratingSum, delivered, cancelled }),
      reviewCount,
      avgRating: reviewCount ? Math.round((ratingSum / reviewCount) * 100) / 100 : null,
      delivered,
      cancelled,
    });
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

module.exports = { createApp };

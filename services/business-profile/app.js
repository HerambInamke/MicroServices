const express = require('express');
const mongoose = require('mongoose');
const { Profile, BUSINESS_TYPES, view } = require('./models');

const EDITABLE = ['businessName', 'gstNumber', 'businessType', 'images'];

const ok = (res, status, data, meta = {}) => res.status(status).json({ data, error: null, meta });
const fail = (res, status, code, message) => res.status(status).json({ data: null, error: { code, message }, meta: {} });

function pick(body, keys) {
  return Object.fromEntries(keys.filter((k) => body[k] !== undefined).map((k) => [k, body[k]]));
}

// Identity headers are set by the gateway after it verifies the JWT.
function requireUser(req, res, next) {
  const userId = req.get('x-user-id');
  if (!userId) return fail(res, 401, 'UNAUTHENTICATED', 'authentication required');
  req.user = { id: userId, role: req.get('x-user-role') || '' };
  next();
}

function requireOwner(req, res, next) {
  if (req.user.id !== req.params.userId) return fail(res, 403, 'FORBIDDEN', 'you can only change your own profile');
  next();
}

function parsePaging(query) {
  const page = query.page === undefined ? 1 : Number(query.page);
  const limit = query.limit === undefined ? 20 : Number(query.limit);
  if (!Number.isInteger(page) || page < 1 || !Number.isInteger(limit) || limit < 1 || limit > 100) return null;
  return { page, limit };
}

const escapeRegex = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// Wraps async handlers so rejected promises reach the error handler.
const wrap = (fn) => (req, res, next) => fn(req, res, next).catch(next);

function createApp() {
  const app = express();
  app.disable('x-powered-by');
  app.use(express.json({ limit: '100kb' }));

  app.get('/health', (req, res) => {
    res.status(200).json({ status: 'ok', service: 'business-profile' });
  });

  app.post('/profiles', requireUser, wrap(async (req, res) => {
    const fields = pick(req.body, EDITABLE);
    if (!fields.businessType && BUSINESS_TYPES.includes(req.user.role)) fields.businessType = req.user.role;
    const profile = await Profile.create({ ...fields, userId: req.user.id });
    ok(res, 201, view(profile));
  }));

  app.get('/profiles', wrap(async (req, res) => {
    const paging = parsePaging(req.query);
    if (!paging) return fail(res, 400, 'INVALID_PAGING', 'page must be >= 1 and limit between 1 and 100');
    const filter = {};
    if (typeof req.query.businessType === 'string') filter.businessType = req.query.businessType;
    if (typeof req.query.q === 'string' && req.query.q.trim()) {
      filter.businessName = { $regex: escapeRegex(req.query.q.trim()), $options: 'i' };
    }
    const [items, total] = await Promise.all([
      Profile.find(filter).sort({ businessName: 1 }).skip((paging.page - 1) * paging.limit).limit(paging.limit),
      Profile.countDocuments(filter),
    ]);
    ok(res, 200, items.map(view), { ...paging, total, hasMore: paging.page * paging.limit < total });
  }));

  app.get('/profiles/:userId', wrap(async (req, res) => {
    const profile = await Profile.findOne({ userId: req.params.userId });
    if (!profile) return fail(res, 404, 'PROFILE_NOT_FOUND', 'profile not found');
    ok(res, 200, view(profile));
  }));

  app.patch('/profiles/:userId', requireUser, requireOwner, wrap(async (req, res) => {
    const profile = await Profile.findOneAndUpdate(
      { userId: req.params.userId },
      { $set: pick(req.body, EDITABLE) },
      { new: true, runValidators: true },
    );
    if (!profile) return fail(res, 404, 'PROFILE_NOT_FOUND', 'profile not found');
    ok(res, 200, view(profile));
  }));

  app.post('/profiles/:userId/certifications', requireUser, requireOwner, wrap(async (req, res) => {
    const profile = await Profile.findOne({ userId: req.params.userId });
    if (!profile) return fail(res, 404, 'PROFILE_NOT_FOUND', 'profile not found');
    profile.certifications.push(pick(req.body, ['name', 'issuer', 'documentUrl']));
    await profile.save();
    ok(res, 201, view(profile));
  }));

  app.use((req, res) => fail(res, 404, 'NOT_FOUND', 'no such route'));

  // eslint-disable-next-line no-unused-vars
  app.use((err, req, res, next) => {
    if (err.type === 'entity.parse.failed') return fail(res, 400, 'INVALID_BODY', 'request body must be valid JSON');
    if (err instanceof mongoose.Error.ValidationError || err instanceof mongoose.Error.CastError) {
      return fail(res, 400, 'VALIDATION_ERROR', err.message);
    }
    if (err.code === 11000) return fail(res, 409, 'PROFILE_EXISTS', 'this user already has a profile');
    console.error(err);
    fail(res, 500, 'INTERNAL', 'something went wrong');
  });

  return app;
}

module.exports = { createApp };

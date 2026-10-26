const mongoose = require('mongoose');

const threadSchema = new mongoose.Schema({
  wholesalerId: { type: String, required: true },
  retailerId: { type: String, required: true },
  lastMessageAt: { type: Date, default: Date.now },
});
threadSchema.index({ wholesalerId: 1, retailerId: 1 }, { unique: true });
threadSchema.index({ retailerId: 1, lastMessageAt: -1 });
threadSchema.index({ wholesalerId: 1, lastMessageAt: -1 });

const OFFER_STATUSES = ['open', 'accepted', 'declined', 'countered'];

const offerSchema = new mongoose.Schema(
  {
    productId: { type: String, required: true },
    qty: { type: Number, required: true },
    unitPrice: { type: Number, required: true },
    status: { type: String, required: true, enum: OFFER_STATUSES, default: 'open' },
    respondsTo: { type: mongoose.Schema.Types.ObjectId },
  },
  { _id: false },
);

const messageSchema = new mongoose.Schema({
  threadId: { type: mongoose.Schema.Types.ObjectId, required: true },
  senderId: { type: String, required: true },
  kind: { type: String, required: true, enum: ['text', 'offer'] },
  text: { type: String },
  offer: { type: offerSchema },
  createdAt: { type: Date, default: Date.now },
});
messageSchema.index({ threadId: 1, _id: -1 });

const Thread = mongoose.model('Thread', threadSchema);
const Message = mongoose.model('Message', messageSchema);

const isParticipant = (thread, userId) => thread.wholesalerId === userId || thread.retailerId === userId;
const otherParty = (thread, userId) => (thread.wholesalerId === userId ? thread.retailerId : thread.wholesalerId);

function threadView(t) {
  return { id: t._id.toString(), wholesalerId: t.wholesalerId, retailerId: t.retailerId, lastMessageAt: t.lastMessageAt };
}

function messageView(m) {
  const v = { id: m._id.toString(), threadId: m.threadId.toString(), senderId: m.senderId, kind: m.kind, createdAt: m.createdAt };
  if (m.kind === 'text') v.text = m.text;
  if (m.kind === 'offer') {
    const o = m.offer;
    v.offer = { productId: o.productId, qty: o.qty, unitPrice: o.unitPrice, status: o.status, respondsTo: o.respondsTo ? o.respondsTo.toString() : null };
  }
  return v;
}

module.exports = { Thread, Message, isParticipant, otherParty, threadView, messageView };

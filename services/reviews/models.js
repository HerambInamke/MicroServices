const mongoose = require('mongoose');

const reviewSchema = new mongoose.Schema({
  orderId: { type: String, required: true, unique: true },
  wholesalerId: { type: String, required: true },
  retailerId: { type: String, required: true },
  rating: { type: Number, required: true },
  text: { type: String, default: '' },
  createdAt: { type: Date, default: Date.now },
});
reviewSchema.index({ wholesalerId: 1, createdAt: -1 });

// Final state of each order, from order.status.changed; upserted by orderId so redelivery is harmless.
const outcomeSchema = new mongoose.Schema({
  orderId: { type: String, required: true, unique: true },
  wholesalerId: { type: String, required: true },
  status: { type: String, required: true, enum: ['delivered', 'cancelled'] },
});
outcomeSchema.index({ wholesalerId: 1, status: 1 });

const Review = mongoose.model('Review', reviewSchema);
const OrderOutcome = mongoose.model('OrderOutcome', outcomeSchema);

function reviewView(r) {
  return { id: r._id.toString(), orderId: r.orderId, wholesalerId: r.wholesalerId, retailerId: r.retailerId, rating: r.rating, text: r.text, createdAt: r.createdAt };
}

module.exports = { Review, OrderOutcome, reviewView };

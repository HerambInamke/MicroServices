const { Kafka, logLevel } = require('kafkajs');
const { OrderOutcome } = require('./models');

// Records delivered/cancelled orders. Malformed messages are logged and skipped so they cannot block the partition.
async function handleStatusChanged(value) {
  let payload;
  try {
    payload = JSON.parse(value).payload;
  } catch {
    payload = null;
  }
  const { orderId, wholesalerId, status } = payload || {};
  if (typeof orderId !== 'string' || typeof wholesalerId !== 'string' || !orderId || !wholesalerId) {
    console.error('skipping malformed order.status.changed message:', String(value).slice(0, 200));
    return;
  }
  if (status !== 'delivered' && status !== 'cancelled') return;
  await OrderOutcome.updateOne({ orderId }, { $set: { wholesalerId, status } }, { upsert: true });
}

async function startConsumer(brokers) {
  const consumer = new Kafka({ clientId: 'reviews', brokers: brokers.split(','), logLevel: logLevel.WARN }).consumer({ groupId: 'reviews' });
  await consumer.connect();
  await consumer.subscribe({ topic: 'order.status.changed', fromBeginning: true });
  // A thrown error (e.g. Mongo down) makes kafkajs retry the message instead of losing it.
  await consumer.run({ eachMessage: ({ message }) => handleStatusChanged(message.value) });
  return consumer;
}

module.exports = { handleStatusChanged, startConsumer };

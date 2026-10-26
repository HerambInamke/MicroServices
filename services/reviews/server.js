const mongoose = require('mongoose');
const { createApp } = require('./app');
const { startConsumer } = require('./consumer');

const PORT = process.env.PORT || 8094;

function requireEnv(name) {
  const v = process.env[name];
  if (!v) {
    console.error(`${name} is required`);
    process.exit(1);
  }
  return v;
}

const MONGO_URL = requireEnv('MONGO_URL');
const KAFKA_BROKERS = requireEnv('KAFKA_BROKERS');
const ORDER_URL = requireEnv('ORDER_URL');

// Reads an order from order-payment as the caller, so its own party check applies.
const orders = {
  async get(id, caller) {
    const r = await fetch(`${ORDER_URL}/orders/${encodeURIComponent(id)}`, {
      headers: { 'x-user-id': caller.id, 'x-user-role': caller.role },
      signal: AbortSignal.timeout(5000),
    });
    return { status: r.status, order: r.status === 200 ? (await r.json()).data : null };
  },
};

// The API keeps serving reviews while Kafka is unreachable; the consumer retries in the background.
function consume() {
  startConsumer(KAFKA_BROKERS).catch((err) => {
    console.error('order consumer failed, retrying in 5s:', err.message);
    setTimeout(consume, 5000);
  });
}

mongoose
  .connect(MONGO_URL)
  .then(() => {
    createApp({ orders }).listen(PORT, () => console.log(`Reviews Service listening on port ${PORT}`));
    consume();
  })
  .catch((err) => {
    console.error('could not connect to MongoDB:', err.message);
    process.exit(1);
  });

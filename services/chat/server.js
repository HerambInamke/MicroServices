const mongoose = require('mongoose');
const Redis = require('ioredis');
const { createApp } = require('./app');
const { outbox, kafkaSender, startRelay, redisBroadcaster } = require('./events');
const { chatSockets } = require('./ws');

const PORT = process.env.PORT || 8093;

function requireEnv(name) {
  const v = process.env[name];
  if (!v) {
    console.error(`${name} is required`);
    process.exit(1);
  }
  return v;
}

const MONGO_URL = requireEnv('MONGO_URL');
const REDIS_URL = requireEnv('REDIS_URL');
const KAFKA_BROKERS = requireEnv('KAFKA_BROKERS');
const AUTH_URL = requireEnv('AUTH_URL');

// Looks a user up in auth-identity with the caller's identity; null when they do not exist.
const users = {
  async get(id, caller) {
    const r = await fetch(`${AUTH_URL}/users/${encodeURIComponent(id)}`, {
      headers: { 'x-user-id': caller.id, 'x-user-role': caller.role },
      signal: AbortSignal.timeout(5000),
    });
    if (r.status === 404) return null;
    if (!r.ok) throw new Error(`auth returned ${r.status}`);
    return (await r.json()).data;
  },
};

mongoose
  .connect(MONGO_URL)
  .then(() => {
    startRelay(kafkaSender(KAFKA_BROKERS));
    const app = createApp({ users, outbox, broadcast: redisBroadcaster(new Redis(REDIS_URL)) });
    const server = app.listen(PORT, () => console.log(`Chat Service listening on port ${PORT}`));
    server.on('upgrade', chatSockets(new Redis(REDIS_URL)));
  })
  .catch((err) => {
    console.error('could not connect to MongoDB:', err.message);
    process.exit(1);
  });

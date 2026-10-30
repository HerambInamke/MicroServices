const crypto = require('crypto');
const { Kafka, logLevel } = require('kafkajs');
const { Outbox, RelayLease } = require('./models');

function envelope(eventType, payload) {
  return { eventId: crypto.randomUUID(), timestamp: new Date().toISOString(), sourceService: 'chat', eventType, payload };
}

// Writes the event to the outbox inside the caller's transaction, so it exists exactly when the change commits.
const outbox = {
  async add(session, topic, eventType, key, payload) {
    await Outbox.create([{ topic, key, event: JSON.stringify(envelope(eventType, payload)) }], { session });
  },
};

// send(topic, key, value) resolves once Kafka acknowledged the message and rejects otherwise.
function kafkaSender(brokers) {
  const producer = new Kafka({ clientId: 'chat', brokers: brokers.split(','), logLevel: logLevel.WARN }).producer();
  let ready = null;
  return async (topic, key, value) => {
    try {
      ready = ready || producer.connect();
      await ready;
      await producer.send({ topic, messages: [{ key, value }] });
    } catch (err) {
      ready = null; // reconnect on the next send
      throw err;
    }
  };
}

const LEASE_MS = 10_000;
const owner = crypto.randomUUID();

// Only one chat instance relays at a time, so events leave in the order they were written.
async function holdLease() {
  const now = new Date();
  try {
    const lease = await RelayLease.findOneAndUpdate(
      { _id: 'outbox', $or: [{ owner }, { until: { $lt: now } }] },
      { $set: { owner, until: new Date(now.getTime() + LEASE_MS) } },
      { upsert: true, new: true },
    );
    return lease.owner === owner;
  } catch (err) {
    if (err.code === 11000) return false; // another instance holds it (the upsert lost the race)
    throw err;
  }
}

/**
 * Sends pending events oldest first and deletes each one Kafka acknowledged; stops at the first failure so no later
 * event overtakes it. Delivery is at least once: a crash between the send and the delete resends the same envelope,
 * which consumers de-duplicate on eventId. Returns how many were sent.
 */
async function relayOnce(send, batch = 100) {
  if (!(await holdLease())) return 0;
  const rows = await Outbox.find().sort({ _id: 1 }).limit(batch);
  let sent = 0;
  for (const row of rows) {
    try {
      await send(row.topic, row.key, row.event);
    } catch (err) {
      console.error(`kafka did not take ${row.topic} for ${row.key}, will retry:`, err.message);
      break;
    }
    await Outbox.deleteOne({ _id: row._id });
    sent++;
  }
  return sent;
}

function startRelay(send) {
  const tick = async () => {
    let sent = 0;
    try {
      sent = await relayOnce(send);
    } catch (err) {
      console.error('outbox relay failed:', err.message);
    }
    setTimeout(tick, sent > 0 ? 0 : 1000);
  };
  tick();
}

// Fans a saved message or offer change out to the thread's open WebSockets (see ws.js).
function redisBroadcaster(redis) {
  return (threadId, event) =>
    redis.publish(`chat:${threadId}`, JSON.stringify(event)).catch((err) => console.error('could not broadcast to', threadId, err.message));
}

module.exports = { envelope, outbox, kafkaSender, relayOnce, startRelay, redisBroadcaster };

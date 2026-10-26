const crypto = require('crypto');
const { Kafka, logLevel } = require('kafkajs');

function envelope(eventType, payload) {
  return { eventId: crypto.randomUUID(), timestamp: new Date().toISOString(), sourceService: 'chat', eventType, payload };
}

// shortcut: published after the Mongo write without an outbox, so an event is lost if Kafka is down at that moment; add an outbox if consumers need every event.
function kafkaPublisher(brokers) {
  const producer = new Kafka({ clientId: 'chat', brokers: brokers.split(','), logLevel: logLevel.WARN }).producer();
  let ready = null;
  return {
    async publish(topic, eventType, key, payload) {
      try {
        ready = ready || producer.connect();
        await ready;
        await producer.send({ topic, messages: [{ key, value: JSON.stringify(envelope(eventType, payload)) }] });
      } catch (err) {
        ready = null; // reconnect on the next publish
        console.error(`could not publish ${eventType} for ${key}:`, err.message);
      }
    },
  };
}

// Fans a saved message or offer change out to the thread's open WebSockets (see ws.js).
function redisBroadcaster(redis) {
  return (threadId, event) =>
    redis.publish(`chat:${threadId}`, JSON.stringify(event)).catch((err) => console.error('could not broadcast to', threadId, err.message));
}

module.exports = { envelope, kafkaPublisher, redisBroadcaster };

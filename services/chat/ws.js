const { WebSocketServer } = require('ws');
const { loadThread } = require('./app');

const PING_MS = 30_000;

/**
 * Returns a server 'upgrade' handler for /ws/chat/:threadId. The socket only receives:
 * sending goes through REST. `subscriber` is a dedicated ioredis connection in subscriber mode.
 */
function chatSockets(subscriber) {
  const wss = new WebSocketServer({ noServer: true });
  const watchers = new Map(); // threadId → Set<WebSocket>

  subscriber.on('message', (channel, payload) => {
    for (const ws of watchers.get(channel.slice('chat:'.length)) || []) ws.send(payload);
  });

  function watch(threadId, ws) {
    if (!watchers.has(threadId)) {
      watchers.set(threadId, new Set());
      subscriber.subscribe(`chat:${threadId}`).catch((err) => console.error('subscribe failed:', err.message));
    }
    watchers.get(threadId).add(ws);
    ws.on('close', () => {
      const set = watchers.get(threadId);
      set.delete(ws);
      if (set.size === 0) {
        watchers.delete(threadId);
        subscriber.unsubscribe(`chat:${threadId}`).catch(() => {});
      }
    });
  }

  // Close sockets that stopped answering pings (e.g. a dropped mobile connection).
  const timer = setInterval(() => {
    for (const ws of wss.clients) {
      if (!ws.alive) ws.terminate();
      else {
        ws.alive = false;
        ws.ping();
      }
    }
  }, PING_MS);
  timer.unref();

  const reject = (socket, status, text) => socket.end(`HTTP/1.1 ${status} ${text}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`);

  return async (req, socket, head) => {
    socket.on('error', () => socket.destroy());
    const match = /^\/ws\/chat\/([^/?]+)(\?|$)/.exec(req.url);
    if (!match) return reject(socket, 404, 'Not Found');
    const userId = req.headers['x-user-id'];
    if (!userId) return reject(socket, 401, 'Unauthorized');
    let thread;
    try {
      thread = await loadThread(decodeURIComponent(match[1]), userId);
    } catch (err) {
      if (err.status === 404) return reject(socket, 404, 'Not Found');
      if (err.status === 403) return reject(socket, 403, 'Forbidden');
      console.error(err);
      return reject(socket, 500, 'Internal Server Error');
    }
    wss.handleUpgrade(req, socket, head, (ws) => {
      ws.alive = true;
      ws.on('pong', () => (ws.alive = true));
      ws.on('error', () => ws.terminate());
      watch(thread._id.toString(), ws);
    });
  };
}

module.exports = { chatSockets };

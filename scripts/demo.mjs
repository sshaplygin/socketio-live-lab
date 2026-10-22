import { randomUUID } from 'node:crypto';
import io from 'socket.io-client';

// This ordinary client needs no bot or metrics credentials.
const endpoint = process.env.LAB_SERVER_URL || 'http://127.0.0.1:8080';
const socket = io(`${endpoint}/lab`, { autoConnect: false, forceNew: true, reconnection: false, timeout: 5000 });
const events = [];
socket.on('lab:event', event => events.push(event));
function ack(event, payload = {}) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${event}: acknowledgement timed out`)), 5000);
    socket.emit(event, payload, reply => {
      clearTimeout(timer);
      if (!reply?.ok) reject(new Error(`${reply?.error?.code}: ${reply?.error?.message}`));
      else resolve(reply.data);
    });
  });
}
async function until(check, label) {
  const deadline = Date.now() + 5000;
  while (!check()) {
    if (Date.now() >= deadline) throw new Error(`${label}: timed out`);
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}
try {
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Connection timed out')), 6000);
    socket.once('connect', () => { clearTimeout(timer); resolve(); });
    socket.once('connect_error', error => { clearTimeout(timer); reject(error); });
    socket.connect();
  });
  const snapshot = await ack('lab:bootstrap', { visitorId: randomUUID() });
  console.log(`Connected as ${snapshot.identity.displayName}`);
  console.log('Connections:', snapshot.presence.connections);
  const started = performance.now();
  await ack('lab:probe');
  console.log(`RTT: ${(performance.now() - started).toFixed(1)} ms`);
  if (!snapshot.presence.botReady) throw new Error('Go bot is not ready. Start it, then rerun the demo.');
  const sent = await ack('lab:send', { clientMessageId: randomUUID(), scope: 'global', text: '/quote' });
  await until(() => events.some(event => event.replyTo === sent.eventId && event.sender.type === 'go-client'), 'Go bot reply');
  const reply = events.find(event => event.replyTo === sent.eventId && event.sender.type === 'go-client');
  console.log(`${reply.sender.name}: ${reply.payload.text}`);
  console.log(`Transport: ${socket.io.engine.transport.name}`);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
} finally {
  socket.disconnect();
}

import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import io from 'socket.io-client';
import http from 'node:http';

const tmp = mkdtempSync(`${tmpdir()}/live-lab-tests-`);
const endpoint = 'http://127.0.0.1:18080';
const token = 'integration-only-bot-token';
let server, bot;
const clients = new Set();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn, timeout = 8000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { if (await fn()) return; await delay(30); }
  assert.fail('Condition timed out');
}
async function status() { return (await fetch(`${endpoint}/api/live-lab/status`)).json(); }
function launchServer() {
  server = spawn(`${tmp}/server`, [], { env: { ...process.env, LAB_ADDR: '127.0.0.1:18080', LAB_ALLOWED_HOSTS: '127.0.0.1:18080', LAB_BOT_TOKEN: token, LAB_HISTORY_LIMIT: '10', LAB_METRICS_TOKEN: token }, stdio: ['ignore', 'pipe', 'pipe'] });
  server.stderr.on('data', d => process.stderr.write(d));
}
async function stop(child) { if (!child || child.exitCode !== null) return; const exit = new Promise(resolve => child.once('exit', resolve)); child.kill('SIGTERM'); await exit; }
function ack(socket, event, data = {}) {
  return new Promise((resolve, reject) => { const timer = setTimeout(() => reject(new Error(`${event} ACK timeout`)), 5000); socket.emit(event, data, value => { clearTimeout(timer); resolve(value); }); });
}
async function connect(options = {}, visitorId = randomUUID()) {
  const socket = io(`${endpoint}/lab`, { forceNew: true, reconnection: false, ...options }); clients.add(socket);
  await new Promise((resolve, reject) => { socket.once('connect', resolve); socket.once('connect_error', reject); });
  socket.events = []; socket.presences = [];
  socket.on('lab:event', e => socket.events.push(e)); socket.on('lab:presence', p => socket.presences.push(p));
  const bootstrap = await ack(socket, 'lab:bootstrap', { visitorId }); assert.equal(bootstrap.ok, true); socket.bootstrap = bootstrap.data;
  return socket;
}
before(async () => {
  execFileSync('go', ['build', '-race', '-o', `${tmp}/server`, './cmd/server'], { cwd: new URL('../', import.meta.url) });
  execFileSync('go', ['build', '-race', '-o', `${tmp}/bot`, './cmd/bot'], { cwd: new URL('../', import.meta.url) });
  launchServer(); await until(async () => { try { return (await fetch(`${endpoint}/healthz`)).ok; } catch { return false; } });
});
after(async () => { for (const c of clients) c.disconnect(); await stop(bot); await stop(server); rmSync(tmp, { recursive: true, force: true }); });

test('real JS clients: authoritative presence, ACKs, transport, dedup, rooms, identity and validation', async () => {
  const a = await connect(); const b = await connect(); const outsider = await connect({ transports: ['polling'], upgrade: false });
  await until(() => a.presences.at(-1)?.connections.total === 3);
  assert.equal((await status()).connections.browser, 3);
  await until(() => a.io.engine.transport.name === 'websocket'); assert.equal(outsider.io.engine.transport.name, 'polling');
  const before = performance.now(); assert.equal((await ack(a, 'lab:probe')).ok, true); assert.ok(performance.now() > before);
  assert.equal((await status()).lastHumanActivityAt, null);
  const request = { clientMessageId: randomUUID(), text: '<script>safe text</script>', scope: 'global' };
  const receipt = await ack(a, 'lab:send', request); assert.equal(receipt.ok, true);
  await until(() => b.events.some(e => e.id === receipt.data.eventId));
  assert.deepEqual(await ack(a, 'lab:send', request), receipt); await delay(100);
  assert.equal(a.events.filter(e => e.id === receipt.data.eventId).length, 1);
  assert.deepEqual(a.events.find(e => e.id === receipt.data.eventId), b.events.find(e => e.id === receipt.data.eventId));
  assert.equal((await ack(a, 'lab:send', { ...request, text: 'changed' })).error.code, 'ID_CONFLICT');
  for (const socket of [a, b]) assert.equal((await ack(socket, 'lab:room:join', { roomId: 'developers' })).ok, true);
  const globalPosition = (await ack(a, 'lab:resync')).data.positions.global;
  const room = await ack(a, 'lab:send', { clientMessageId: randomUUID(), text: 'room only', scope: 'room', roomId: 'developers' });
  await until(() => b.events.some(e => e.id === room.data.eventId)); await delay(100); assert.ok(!outsider.events.some(e => e.id === room.data.eventId));
  assert.equal((await ack(a, 'lab:resync')).data.positions.global, globalPosition);
  await ack(b, 'lab:room:join', { roomId: 'experiments' });
  const isolated = await ack(a, 'lab:send', { clientMessageId: randomUUID(), text: 'after switch', scope: 'room', roomId: 'developers' });
  await delay(100); assert.ok(!b.events.some(e => e.id === isolated.data.eventId));
  assert.equal((await status()).rooms.developers, 1);
  const sameIdentity = await connect({}, a.bootstrap.identity.visitorId);
  assert.equal(sameIdentity.bootstrap.identity.displayName, a.bootstrap.identity.displayName);
  const renamed = await ack(a, 'lab:identity:shuffle'); assert.equal(renamed.ok, true);
  assert.equal((await ack(sameIdentity, 'lab:resync')).data.identity.displayName, renamed.data.displayName);
  assert.equal((await ack(a, 'lab:room:join', { roomId: 'private' })).error.code, 'INVALID_ROOM');
  for (const payload of [null, 'text', { text: 42 }, { text: 'x'.repeat(501), clientMessageId: randomUUID(), scope: 'global' }]) assert.equal((await ack(a, 'lab:send', payload)).ok, false);
  assert.equal((await ack(a, 'lab:bot:ready')).error.code, 'FORBIDDEN');
  assert.equal((await ack(a, 'lab:bot:echo', { text: 'no bot' })).error.code, 'BOT_UNAVAILABLE');
  let limited = false;for (let i = 0; i < 7; i++) { const res = await ack(a, 'lab:send', { clientMessageId: randomUUID(), text: 'rate check', scope: 'global' }); if (res.error?.code === 'RATE_LIMITED') limited = true; }assert.ok(limited);
  outsider.disconnect(); await until(async () => (await status()).connections.total === 3);
  for (const socket of [a, b, sameIdentity]) socket.disconnect(); await until(async () => (await status()).connections.total === 0);
});

test('reconnect, lost ACK retry, bounded history gaps and server restart', async () => {
  const id = randomUUID(); const a = await connect({}, id); await ack(a, 'lab:room:join', { roomId: 'general' });
  const request = { clientMessageId: randomUUID(), text: 'accepted before disconnect', scope: 'global' };
  a.emit('lab:send', request); await until(() => a.events.some(e => e.payload.text === request.text));
  const original = a.events.find(e => e.payload.text === request.text); const boot = a.bootstrap.bootId; a.disconnect();
  const b = await connect({}, id); await ack(b, 'lab:room:join', { roomId: 'general' });
  const retry = await ack(b, 'lab:send', request); assert.equal(retry.data.eventId, original.id); assert.equal((await status()).rooms.general, 1);
  for (let i = 0; i < 12; i++) { const c = await connect(); c.disconnect(); }
  const snapshot = await ack(b, 'lab:resync', { bootId: boot, positions: { global: 1 } }); assert.equal(snapshot.data.truncated, true); assert.ok(snapshot.data.history.filter(e => e.scope === 'global').length <= 10);
  await stop(server); launchServer(); await until(async () => { try { return (await status()).bootId !== boot; } catch { return false; } });
  const c = await connect({}, id); const fresh = await ack(c, 'lab:resync', { bootId: boot, positions: snapshot.data.positions }); assert.equal(fresh.data.truncated, true); assert.equal(fresh.data.presence.connections.total, 1);
  c.disconnect(); b.disconnect(); await until(async () => (await status()).connections.total === 0);
});

test('separate Go process: readiness, correlated echo, no manufactured activity and restart', async () => {
  const a = await connect();
  const startBot = () => { bot = spawn(`${tmp}/bot`, [], { env: { ...process.env, LAB_SERVER_URL: endpoint, LAB_BOT_TOKEN: token }, stdio: ['ignore', 'pipe', 'pipe'] });bot.stderr.on('data', d => process.stderr.write(d)); };
  const previous = (await status()).lastHumanActivityAt; startBot(); await until(async () => (await status()).botReady);
  assert.equal((await status()).lastHumanActivityAt, previous); assert.equal((await status()).connections.goClient, 1);
  const result = new Promise(resolve => a.once('lab:bot:result', resolve)); const accepted = await ack(a, 'lab:bot:echo', { text: 'hello Go' }); assert.equal(accepted.ok, true);
  const humanTime = (await status()).lastHumanActivityAt; const echo = await result; assert.equal(echo.correlationId, accepted.data.correlationId); assert.equal(echo.text, 'hello Go');
  await until(() => a.events.some(e => e.type === 'bot')); assert.equal((await status()).lastHumanActivityAt, humanTime);
  await stop(bot); await until(async () => !(await status()).botReady); assert.equal((await status()).connections.goClient, 0);
  startBot(); await until(async () => (await status()).botReady);
  const oldBoot = (await status()).bootId; await stop(server); launchServer();
  await until(async () => { try { const s = await status(); return s.bootId !== oldBoot && s.botReady; } catch { return false; } }, 15000);
  assert.equal((await status()).lastHumanActivityAt, null);
  await stop(bot);a.disconnect();
});

test('Go client answers human chat messages, keeps room scope and stays quiet alone', async () => {
  bot = spawn(`${tmp}/bot`, [], { env: { ...process.env, LAB_SERVER_URL: endpoint, LAB_BOT_TOKEN: token }, stdio: ['ignore', 'pipe', 'pipe'] });
  await until(async () => (await status()).botReady);
  const a = await connect(); const outsider = await connect();
  try {
    await ack(a, 'lab:room:join', { roomId: 'developers' });
    const sent = await ack(a, 'lab:send', { clientMessageId: randomUUID(), scope: 'room', roomId: 'developers', text: '/ping' });
    assert.equal(sent.ok, true);
    const humanTime = (await status()).lastHumanActivityAt;
    await until(() => a.events.some(e => e.replyTo === sent.data.eventId));
    const reply = a.events.find(e => e.replyTo === sent.data.eventId);
    assert.equal(reply.sender.type, 'go-client'); assert.equal(reply.scope, 'room'); assert.equal(reply.roomId, 'developers');
    assert.match(reply.payload.text, /pong!/); assert.equal((await status()).lastHumanActivityAt, humanTime);
    assert.ok(!outsider.events.some(e => e.replyTo === sent.data.eventId));
    const joke = await ack(a, 'lab:send', { clientMessageId: randomUUID(), scope: 'room', roomId: 'developers', text: '/joke' });
    assert.equal(joke.ok, true);
    await until(() => a.events.some(e => e.replyTo === joke.data.eventId));
    assert.match(a.events.find(e => e.replyTo === joke.data.eventId).payload.text, /goroutine/);
    let previousQuote;
    for (const text of ['/quote', 'Tell me something interesting.']) {
      const message = await ack(a, 'lab:send', { clientMessageId: randomUUID(), scope: 'room', roomId: 'developers', text });
      assert.equal(message.ok, true);
      await until(() => a.events.some(e => e.replyTo === message.data.eventId));
      const quote = a.events.find(e => e.replyTo === message.data.eventId);
      assert.equal(quote.sender.type, 'go-client'); assert.equal(quote.roomId, 'developers');
      assert.match(quote.payload.text, /^@\S+ “.+”\n— .+$/u);
      assert.ok([...quote.payload.text].length <= 500);
      assert.notEqual(quote.payload.text, previousQuote);
      previousQuote = quote.payload.text;
      assert.ok(!outsider.events.some(e => e.replyTo === message.data.eventId));
    }
    a.disconnect(); outsider.disconnect(); await until(async () => (await status()).connections.browser === 0);
    const quiet = await status(); await delay(5500);
    const later = await status(); assert.equal(later.lastHumanActivityAt, quiet.lastHumanActivityAt); assert.equal(later.lastEventAt, quiet.lastEventAt);
  } finally { a.disconnect(); outsider.disconnect(); await stop(bot); }
});

test('HTTP origin/host controls and uncached status', async () => {
  const response = await fetch(`${endpoint}/api/live-lab/status`); assert.equal(response.headers.get('cache-control'), 'no-store'); const json = await response.json();assert.ok(!JSON.stringify(json).includes('visitorId'));
  assert.equal((await fetch(`${endpoint}/api/live-lab/status`, { headers: { Origin: 'https://evil.example' } })).status, 403);
  assert.equal(await new Promise(resolve => { http.get(`${endpoint}/api/live-lab/status`, { headers: { Host: 'evil.example' } }, response => { response.resume(); resolve(response.statusCode); }); }), 403);
  assert.equal((await fetch(`${endpoint}/metrics`)).status, 404);
});


test('simultaneous tab reloads keep namespace generations and counts consistent', async () => {
  const id = randomUUID(); let tabs = await Promise.all(Array.from({ length: 6 }, () => connect({}, id)));
  assert.equal((await status()).connections.browser, 6);
  for (const c of tabs) c.disconnect();
  tabs = await Promise.all(Array.from({ length: 6 }, () => connect({}, id)));
  await until(async () => (await status()).connections.browser === 6);
  const snapshot = (await ack(tabs[0], 'lab:resync')).data;
  assert.equal(snapshot.presence.participants.length, 1); assert.equal(snapshot.presence.participants[0].connections, 6);
  for (const c of tabs) c.disconnect(); await until(async () => (await status()).connections.total === 0);
});

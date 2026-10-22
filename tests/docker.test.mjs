import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import io from 'socket.io-client';

const exec = promisify(execFile);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, timeout = 20_000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    try { if (await check()) return; } catch { /* The container may be restarting. */ }
    await delay(100);
  }
  assert.fail('Container readiness condition timed out');
}
async function freePort() {
  const listener = createServer();
  await new Promise((resolve, reject) => { listener.once('error', reject); listener.listen(0, '127.0.0.1', resolve); });
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  return port;
}
function ack(socket, name, payload = {}) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${name}: ACK timeout`)), 5000);
    socket.emit(name, payload, response => { clearTimeout(timer); resolve(response); });
  });
}
function event(socket, name) {
  return new Promise((resolve, reject) => {
    const listener = value => { clearTimeout(timer); resolve(value); };
    const timer = setTimeout(() => { socket.removeListener(name, listener); reject(new Error(`${name}: event timeout`)); }, 5000);
    socket.once(name, listener);
  });
}

test('Docker server and bot exchange real events and recover from container restarts', { timeout: 300_000 }, async t => {
  const directory = await mkdtemp(`${tmpdir()}/live-lab-docker-`);
  const port = await freePort();
  const endpoint = `http://127.0.0.1:${port}`;
  const envFile = `${directory}/.env`;
  await writeFile(envFile, `LAB_HTTP_PORT=${port}\nLAB_BOT_TOKEN=${randomBytes(32).toString('hex')}\nLAB_METRICS_TOKEN=${randomBytes(32).toString('hex')}\n`, { mode: 0o600 });
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('LAB_')));
  const composeFile = fileURLToPath(new URL('../compose.yaml', import.meta.url));
  const compose = async (...args) => (await exec('docker', ['compose', '--project-name', `live-lab-test-${process.pid}`, '--env-file', envFile, '-f', composeFile, ...args], { env: environment, timeout: 240_000, maxBuffer: 4 * 1024 * 1024 })).stdout;
  const status = async () => (await fetch(`${endpoint}/api/live-lab/status`, { signal: AbortSignal.timeout(3000) })).json();
  const sockets = [];
  async function connect(options = {}) {
    const socket = io(`${endpoint}/lab`, { forceNew: true, reconnection: false, extraHeaders: { Origin: 'http://localhost:4321' }, ...options });
    sockets.push(socket);
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Container connection timed out')), 5000);
      socket.once('connect', () => { clearTimeout(timer); resolve(); });
      socket.once('connect_error', error => { clearTimeout(timer); reject(error); });
    });
    assert.equal((await ack(socket, 'lab:bootstrap', { visitorId: randomUUID() })).ok, true);
    return socket;
  }
  try {
    await compose('config', '--quiet');
    await compose('up', '-d', '--build', '--wait', '--wait-timeout', '60');
    await until(async () => (await status()).botReady);
    assert.deepEqual((await status()).connections, { total: 1, browser: 0, goClient: 1 });
    assert.equal((await compose('port', 'server', '8080')).trim(), `127.0.0.1:${port}`);
    for (const service of ['server', 'bot']) assert.equal((await compose('exec', '-T', service, 'id', '-u')).trim(), '10001');
    await compose('exec', '-T', 'server', 'sh', '-c', 'test ! -e /usr/local/bin/bot');
    await compose('exec', '-T', 'bot', 'sh', '-c', 'test ! -e /usr/local/bin/server');
    t.diagnostic('Separate unprivileged images, localhost binding and bot readiness verified.');

    const sender = await connect();
    const receiver = await connect({ transports: ['polling'], upgrade: false });
    await until(() => sender.io.engine.transport.name === 'websocket');
    assert.equal(receiver.io.engine.transport.name, 'polling');
    assert.deepEqual((await status()).connections, { total: 3, browser: 2, goClient: 1 });
    assert.equal((await ack(sender, 'lab:probe')).ok, true);
    // Presence system events may still arrive, so select the exact submitted content.
    const messages = []; receiver.on('lab:event', value => messages.push(value));
    const sent = await ack(sender, 'lab:send', { clientMessageId: randomUUID(), scope: 'global', text: 'Real event through Docker' });
    assert.equal(sent.ok, true);
    await until(() => messages.some(value => value.id === sent.data.eventId));
    const reply = event(sender, 'lab:bot:result');
    const accepted = await ack(sender, 'lab:bot:echo', { text: 'Docker echo' });
    const result = await reply;
    assert.equal(accepted.ok, true);
    assert.equal(result.correlationId, accepted.data.correlationId);
    assert.equal(result.text, 'Docker echo');
    t.diagnostic('WebSocket, polling, presence, message delivery, probe ACK and Go echo verified.');

    await compose('stop', 'bot');
    await until(async () => !(await status()).botReady);
    assert.equal((await ack(sender, 'lab:bot:echo', { text: 'Bot is stopped' })).error.code, 'BOT_UNAVAILABLE');
    await compose('start', 'bot');
    await until(async () => (await status()).botReady);
    const previousBoot = (await status()).bootId;
    await compose('restart', 'server');
    await until(async () => { const current = await status(); return current.bootId !== previousBoot && current.botReady; });
    await until(async () => (await status()).connections.total === 1);
    const fresh = await connect();
    const nextReply = event(fresh, 'lab:bot:result');
    assert.equal((await ack(fresh, 'lab:bot:echo', { text: 'After server restart' })).ok, true);
    assert.equal((await nextReply).text, 'After server restart');
    t.diagnostic('Bot stop/start and server restart recover readiness and real Go echo.');
  } finally {
    for (const socket of sockets) socket.disconnect();
    try { await compose('down', '--volumes', '--remove-orphans'); }
    finally { await rm(directory, { recursive: true, force: true }); }
  }
});

const { test } = require('node:test');
const assert = require('node:assert/strict');
const browser = require('./fixtures/browser.cjs');

const hello = { type: 'hello', name: 'Alice', room: 'ABCDE', create: false, token: 'secret' };
const joined = { type: 'joined', roomId: 'ABCDE', roomStatus: 'lobby', self: { id: 'a' }, token: 'secret' };

test('the shell opens immediately and a later hello reuses that transport', () => {
  const f = browser();
  assert.equal(f.sockets.length, 1);
  assert.equal(f.sockets[0].url, 'wss://example.test/ws');
  assert.equal(f.requests.length, 0);
  f.sockets[0].open();
  f.advance(10000);
  assert.equal(f.sockets[0].sent[0].type, 'ping');
  f.sockets[0].message({ type: 'pong' });
  f.connection.join(hello);
  assert.equal(f.sockets.length, 1);
  assert.equal(f.connection.phase, 'joining');
  assert.equal(f.sockets[0].sent.at(-1).token, 'secret');
  f.sockets[0].message(joined);
  assert.equal(f.connection.phase, 'ready');
});

test('a cached session sends hello before the UI loads and never waits on HTTP', () => {
  const f = browser({ pathname: '/r/abcde', storage: {
    'uno:player:ABCDE': JSON.stringify({ name: 'Alice', token: 'secret' }),
  } });
  assert.equal(f.sockets.length, 1);
  assert.equal(f.requests.length, 0);
  f.sockets[0].open();
  assert.equal(f.sockets[0].sent[0].room, 'ABCDE');
  assert.equal(f.sockets[0].sent[0].token, 'secret');
});

test('invalid storage falls back to a warm transport', () => {
  for (const value of ['{bad', 'null', '{"name":{},"token":"secret"}', '{"name":"","token":"secret"}']) {
    const f = browser({ pathname: '/r/ABCDE', storage: { 'uno:player:ABCDE': value } });
    f.sockets[0].open();
    assert.equal(f.connection.hello, null);
    assert.equal(f.connection.phase, 'idle');
  }
});

test('submitting during the handshake and duplicate recovery events keep one socket', () => {
  const f = browser();
  const socket = f.sockets[0];
  f.connection.join(hello);
  f.connection.join(hello);
  for (const [scope, event] of [['window', 'pageshow'], ['window', 'online'], ['document', 'resume'], ['document', 'visibilitychange']]) {
    f.event(scope, event);
  }
  assert.equal(f.connection.ws, socket);
  assert.equal(f.sockets.length, 1);
  socket.open();
  assert.equal(socket.sent.length, 1);
});

test('handshake and first authoritative snapshot time out after two seconds and retry', () => {
  for (const stage of ['opening', 'hello', 'playing']) {
    const f = browser();
    f.connection.join(hello);
    const socket = f.sockets[0];
    if (stage !== 'opening') {
      f.advance(1500);
      socket.open();
      socket.message({ type: 'heartbeat' });
      if (stage === 'playing') socket.message({ ...joined, roomStatus: 'playing' });
      assert.equal(f.connection.send({ type: 'start' }), false);
      f.advance(1999);
      assert.equal(f.connection.ws, socket);
      f.advance(1);
    } else {
      f.advance(1999);
      assert.equal(f.connection.ws, socket);
      f.advance(1);
    }
    assert.equal(f.connection.ws, null, stage);
    assert.ok(f.connection.retry, stage);
    const delay = f.timers.get(f.connection.retry).delay;
    assert.ok(delay >= 150 && delay <= 250);
    f.advance(delay);
    const current = f.connection.ws;
    assert.notEqual(current, socket);
    assert.equal(f.sockets.length, 2);
    socket.emit('open'); socket.message(joined); socket.emit('error'); socket.emit('close');
    assert.equal(f.connection.ws, current, 'timed-out sockets cannot disturb the retry');
  }
});

test('playing rooms become ready only after state and new rounds wait for state again', () => {
  const f = browser(); f.connection.join(hello);
  const socket = f.sockets[0]; socket.open();
  socket.message({ ...joined, roomStatus: 'playing' });
  assert.equal(f.connection.send({ type: 'draw' }), false);
  socket.message({ type: 'state' });
  assert.equal(f.connection.send({ type: 'draw' }), true);
  socket.message({ type: 'started' });
  assert.equal(f.connection.send({ type: 'draw' }), false);
  f.advance(2000);
  assert.equal(f.connection.ws, null);
});

test('HTTP validation never blocks retry and its late response cannot erase a recovered session', async () => {
  const f = browser(); f.connection.join(hello);
  const socket = f.sockets[0]; socket.open(); socket.message(joined);
  socket.emit('error');
  assert.equal(f.requests.length, 1);
  assert.equal(f.requests[0].options.cache, 'no-store');
  const delay = f.timers.get(f.connection.retry).delay;
  assert.ok(delay >= 150 && delay <= 250);
  f.advance(delay);
  const current = f.connection.ws;
  assert.notEqual(current, socket);
  current.open(); current.message(joined);
  assert.equal(f.requests[0].options.signal.aborted, true);
  f.requests[0].resolve({ status: 404 });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.connection.ws, current);
  assert.equal(f.connection.phase, 'ready');
});

test('confirmed invalid sessions stop retries and return an explicit error', async () => {
  for (const status of [401, 404]) {
    const f = browser(); const messages = [];
    f.connection.subscribe(() => {}, message => messages.push(message));
    f.connection.join(hello); f.sockets[0].emit('error');
    f.requests[0].resolve({ status });
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(f.connection.ws, null);
    assert.equal(f.connection.retry, null);
    assert.equal(f.connection.hello, null);
    assert.equal(messages[0].code, status === 401 ? 'session_invalid' : 'room_not_found');
  }
});

test('validation times out independently and stale results cannot erase a different room', async () => {
  const f = browser(); f.connection.join(hello); f.sockets[0].emit('error');
  f.advance(4000);
  assert.equal(f.requests[0].options.signal.aborted, true);
  assert.ok(f.connection.ws);
  f.connection.join({ ...hello, room: 'FGHIJ', token: 'other' });
  f.requests[0].resolve({ status: 401 });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(f.connection.hello.room, 'FGHIJ');
});

test('retry starts fast, backs off with jitter, and resets only after authoritative state', () => {
  const f = browser(); f.connection.join(hello);
  for (let attempt = 0; attempt < 9; attempt++) {
    f.connection.ws.emit('error');
    const delay = f.timers.get(f.connection.retry).delay;
    const base = Math.min(200 * 2 ** Math.min(attempt, 6), 10000);
    assert.ok(delay >= base * 0.75 && delay <= base * 1.25);
    f.advance(delay);
    assert.equal(f.connection.attempts, attempt + 1);
  }
  f.connection.ws.open();
  f.connection.ws.message({ ...joined, roomStatus: 'playing' });
  assert.equal(f.connection.attempts, 9);
  f.connection.ws.message({ type: 'state' });
  assert.equal(f.connection.attempts, 0);
});

test('offline startup does no work and network recovery starts one immediate attempt', () => {
  const f = browser({ online: false });
  f.connection.join(hello);
  assert.equal(f.sockets.length, 0);
  assert.equal(f.timers.size, 0);
  f.navigator.onLine = true;
  f.event('window', 'online'); f.event('document', 'resume'); f.event('window', 'pageshow');
  assert.equal(f.sockets.length, 1);
});

test('offline cancels socket, retry, heartbeat, and session validation work', () => {
  const f = browser(); f.connection.join(hello);
  f.sockets[0].emit('error');
  f.navigator.onLine = false; f.event('window', 'offline');
  assert.equal(f.connection.ws, null);
  assert.equal(f.timers.size, 0);
  assert.equal(f.requests[0].options.signal.aborted, true);
  f.advance(60000);
  assert.equal(f.sockets.length, 1);
  f.connection.join(hello);
  assert.equal(f.sockets.length, 1);
  f.navigator.onLine = true; f.event('window', 'online');
  assert.equal(f.sockets.length, 2);
});

test('foreground, page restoration, and resume replace suspended sockets without duplicate attempts', () => {
  for (const event of ['hidden', 'pagehide', 'freeze']) {
    const f = browser(); f.connection.join(hello);
    const old = f.sockets[0]; old.open(); old.message(joined);
    if (event === 'hidden') {
      f.document.visibilityState = 'hidden'; f.event('document', 'visibilitychange');
      f.document.visibilityState = 'visible';
    } else f.event(event === 'freeze' ? 'document' : 'window', event);
    f.event('document', 'visibilitychange'); f.event('document', 'resume'); f.event('window', 'pageshow');
    const current = f.connection.ws;
    assert.notEqual(current, old);
    assert.equal(f.sockets.length, 2);
    old.emit('open'); old.emit('error'); old.emit('close'); old.message({ type: 'error', code: 'room_not_found' });
    assert.equal(f.connection.ws, current);
    assert.equal(f.connection.retry, null);
    current.open();
    assert.equal(current.sent[0].token, 'secret');
    assert.equal(current.sent[0].create, false);
  }
});

test('server heartbeats keep the socket healthy and silence causes recovery', () => {
  const f = browser(); f.connection.join(hello);
  const socket = f.sockets[0]; socket.open(); socket.message(joined);
  for (let n = 0; n < 6; n++) {
    f.advance(10000); socket.message({ type: 'heartbeat' });
  }
  assert.equal(f.connection.ws, socket);
  f.advance(30000);
  assert.equal(f.connection.ws, null);
  assert.ok(f.connection.retry);
});

test('stale sockets detected on resume reconnect before delayed timers fire', () => {
  const f = browser(); f.connection.join(hello);
  const socket = f.sockets[0]; socket.open(); socket.message(joined);
  f.elapse(60000); f.event('document', 'resume');
  assert.notEqual(f.connection.ws, socket);
  assert.equal(f.sockets.length, 2);
});

test('resume retires an expired handshake even when its deadline timer was suspended', () => {
  for (const opened of [false, true]) {
    const f = browser(); f.connection.join(hello);
    const socket = f.sockets[0];
    if (opened) socket.open();
    f.elapse(10000); f.event('document', 'resume');
    f.event('window', 'pageshow');
    assert.notEqual(f.connection.ws, socket);
    assert.equal(f.sockets.length, 2);
  }
});

test('a new room intent aborts the old socket and all its callbacks are ignored', () => {
  const f = browser(); f.connection.join(hello);
  const old = f.sockets[0];
  f.connection.join({ ...hello, room: 'FGHIJ', token: 'other' });
  old.emit('open'); old.message(joined); old.emit('error'); old.emit('close');
  assert.equal(f.sockets.length, 2);
  assert.equal(f.connection.hello.room, 'FGHIJ');
  assert.equal(f.connection.retry, null);
});

test('a rejected hello stops retrying but another submission can start immediately', () => {
  for (const code of ['room_full', 'name_taken', 'game_in_progress', 'room_not_found']) {
    const f = browser(); f.connection.join(hello); f.sockets[0].open();
    f.sockets[0].message({ type: 'error', code, message: 'Rejected' });
    assert.equal(f.connection.retry, null);
    assert.equal(f.connection.hello, null);
    f.connection.join({ ...hello, name: 'Bob' });
    assert.equal(f.sockets.length, 2);
  }
});

test('malformed messages and constructor failures recover through the same retry loop', () => {
  const f = browser();
  f.sockets[0].emit('message', { data: '{bad' });
  assert.equal(f.connection.ws, null);
  assert.ok(f.connection.retry);
  const failed = browser({ throwOnOpen: true });
  assert.ok(failed.connection.retry);
});

test('destroy cancels every callback and timer', () => {
  const f = browser(); f.connection.join(hello); f.sockets[0].emit('error');
  f.connection.destroy();
  assert.equal(f.timers.size, 0);
  assert.equal(Object.keys(f.listeners).length, 0);
  assert.equal(f.requests[0].options.signal.aborted, true);
  f.advance(60000);
  assert.equal(f.sockets.length, 1);
});

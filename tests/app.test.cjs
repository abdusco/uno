const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function fixture() {
  let makeApp;
  let now = 1000;
  let nextTimer = 0;
  const timers = new Map();
  const listeners = {};
  class Socket {
    static OPEN = 1;
    constructor() { this.readyState = 0; this.listeners = {}; this.sent = []; }
    addEventListener(event, fn) { this.listeners[event] = fn; }
    emit(event, data) { this.listeners[event]?.(data); }
    send(data) {
      if (this.readyState !== Socket.OPEN) throw new Error('socket is not open');
      this.sent.push(JSON.parse(data));
    }
    close() { this.readyState = 3; this.emit('close'); }
  }
  const addTimer = (fn, delay) => { const id = ++nextTimer; timers.set(id, { fn, delay }); return id; };
  const window = {
    innerWidth: 800,
    location: { protocol: 'http:', host: 'example.test', origin: 'http://example.test', pathname: '/' },
    addEventListener: (name, fn) => { listeners[name] = fn; },
    removeEventListener() {},
  };
  const document = {
    visibilityState: 'visible',
    addEventListener: (name, fn) => name === 'alpine:init' ? fn() : listeners[name] = fn,
    removeEventListener() {},
  };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/js/app.js'), 'utf8'), {
    window, document, navigator: {}, WebSocket: Socket,
    Alpine: { data: (_, fn) => { makeApp = fn; } },
    Date: { now: () => now }, setTimeout: addTimer, setInterval: addTimer,
    clearTimeout: id => timers.delete(id), clearInterval: id => timers.delete(id),
    localStorage: { getItem() { return null; }, setItem() {} },
    history: { pushState() {}, replaceState() {} },
  });
  const app = makeApp();
  app.$nextTick = fn => fn(); app.$refs = {};
  app.renderQrCode = app.prepareAudio = app.vibrate = app.playCardSfx = app.playUnoSfx = app.startMusic = app.stopMusic = () => {};
  app.name = 'Alice';
  const hello = { type: 'hello', name: 'Alice', room: 'ABCDE', create: false, token: 'secret' };
  function join() {
    app.connect(hello);
    app.ws.readyState = Socket.OPEN;
    app.ws.emit('open');
    app.ws.emit('message', { data: JSON.stringify({ type: 'joined', roomId: 'ABCDE', self: { id: 'a', isHost: true }, token: 'secret', resumed: true }) });
  }
  return { app, join, hello, timers, listeners, advance: ms => { now += ms; } };
}

test('idle sockets send heartbeats and reconnect after missing replies', () => {
  const f = fixture(); f.join();
  const socket = f.app.ws;
  f.advance(10000); f.timers.get(f.app._heartbeatTimer).fn();
  assert.equal(socket.sent.at(-1).type, 'ping');
  f.advance(10000); socket.emit('message', { data: '{"type":"pong"}' });
  f.advance(10000); f.timers.get(f.app._heartbeatTimer).fn();
  assert.equal(f.app.ws, socket);
  f.advance(20000); f.timers.get(f.app._heartbeatTimer).fn();
  assert.equal(f.app.ws, null);
  assert.equal(f.app.status, 'connecting');
  assert.ok(f.app._reconnectTimer);
});

test('a stalled hello times out and retries a cached identity', () => {
  const f = fixture(); f.app.connect(f.hello);
  f.timers.get(f.app._connectionTimer).fn();
  assert.equal(f.app.ws, null);
  assert.ok(f.app._reconnectTimer);
});

test('returning from sleep replaces a stale socket and ignores its late close', () => {
  const f = fixture(); f.app.init(); f.join();
  const old = f.app.ws;
  f.advance(60000); f.listeners.visibilitychange();
  assert.notEqual(f.app.ws, old);
  old.emit('close');
  assert.equal(f.app._reconnectTimer, null);
  assert.equal(f.app.status, 'connecting');
});


test('reconnecting into a rematch removes the old winner and color picker', () => {
  const f = fixture(); f.join();
  f.app.screen = 'game';
  f.app.gameOver = { winnerId: 'b', winnerName: 'Bob' };
  f.app.pendingWildCard = { id: 'previous-round-wild', color: 'wild', value: 'wild' };
  f.app.handleMessage({
    type: 'state', hand: [{ id: 'new-card', color: 'red', value: '1' }],
    gamePlayers: [{ id: 'a', connected: true }, { id: 'b', connected: true }],
    yourTurn: true, currentPlayerId: 'a',
  });
  assert.equal(f.app.gameOver, null);
  assert.equal(f.app.pendingWildCard, null);
  assert.equal(f.app.screen, 'game');
  assert.equal(f.app.hand[0].id, 'new-card');
});

test('rejoining a completed round still shows its winner', () => {
  const f = fixture(); f.join();
  f.app.handleMessage({ type: 'gameOver', winnerId: 'b', winnerName: 'Bob' });
  assert.equal(f.app.gameOver.winnerId, 'b');
});

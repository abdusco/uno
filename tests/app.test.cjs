const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const browser = require('./fixtures/browser.cjs');

function fixture() {
  const f = browser();
  f.app = f.app();
  f.app.name = 'Alice';
  f.hello = { type: 'hello', name: 'Alice', room: 'ABCDE', create: false, token: 'secret' };
  f.join = () => {
    if (f.connection.phase === 'ready') f.app.ws.emit('error');
    f.app.connect(f.hello);
    f.app.ws.open();
    f.app.ws.message({ type: 'joined', roomId: 'ABCDE', self: { id: 'a', isHost: true }, token: 'secret', resumed: true });
  };
  return f;
}

test('Alpine initializes once and consumes the transport opened before the UI', () => {
  const html = fs.readFileSync(path.join(__dirname, '../web/index.html'), 'utf8');
  assert.ok(html.indexOf('/js/connection.js') < html.indexOf('/css/style.css'));
  assert.doesNotMatch(html, /x-init=["']init\(\)["']/);
  const f = browser();
  const socket = f.sockets[0];
  socket.open();
  f.app = f.app();
  assert.equal(f.app.canSend(), false, 'a warm transport cannot accept room actions');
  f.app.name = 'Alice';
  f.app.submitName();
  assert.equal(f.app.ws, socket);
  assert.equal(f.sockets.length, 1);
  assert.equal(socket.sent[0].type, 'hello');
  assert.equal(f.app.canSend(), false, 'an open transport is not an accepted room');
});

test('startup replays snapshots received before Alpine loads', () => {
  const f = browser({ pathname: '/r/ABCDE', storage: {
    'uno:player:ABCDE': JSON.stringify({ name: 'Alice', token: 'secret' }),
  } });
  const socket = f.sockets[0];
  socket.open();
  socket.message({ type: 'joined', roomId: 'ABCDE', roomStatus: 'playing', self: { id: 'a' }, token: 'secret', resumed: true });
  socket.message({ type: 'state', hand: [{ id: 'red', color: 'red', value: '1' }], yourTurn: true,
    gamePlayers: [{ id: 'a', connected: true }, { id: 'b', connected: true }] });
  f.app = f.app();
  assert.equal(f.sockets.length, 1);
  assert.equal(f.app.name, 'Alice');
  assert.equal(f.app.roomId, 'ABCDE');
  assert.equal(f.app.screen, 'game');
  assert.equal(f.app.canAct, true);
  assert.equal(f.app.hand[0].id, 'red');
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

function gameFixture() {
  const f = fixture(); f.join();
  f.app.handleMessage({
    type: 'state',
    hand: [{ id: 'wild', color: 'wild', value: 'wild' }, { id: 'red', color: 'red', value: '1' }],
    discardTop: { id: 'discard', color: 'red', value: '2' }, topColor: 'red',
    gamePlayers: [{ id: 'a', connected: true }, { id: 'b', connected: true, unoCatchable: true }],
    yourTurn: true, currentPlayerId: 'a',
  });
  return f;
}

test('all game actions reject connecting, closing, closed, and unaccepted sockets', () => {
  for (const readyState of [0, 2, 3, 1]) {
    const f = gameFixture(); const socket = f.app.ws;
    socket.sent = [];
    socket.readyState = readyState;
    if (readyState === 1) f.app.status = 'connecting';
    f.app.pendingWildCard = f.app.hand[0];
    f.app.yourDrawnCard = f.app.hand[0];
    for (const action of [
      () => f.app.chooseColor('red'), () => f.app.sendPlay('wild', 'red'),
      () => f.app.playDrawnCard(), () => f.app.keepDrawnCard(),
      () => f.app.catchUno('b'), () => f.app.callUno(),
      () => { f.app.yourDrawnCard = null; f.app.drawCard(); },
      () => { f.app.canChallengeWild4 = true; f.app.acceptWildDrawFour(); f.app.challengeWildDrawFour(); },
      () => { f.app.screen = 'lobby'; f.app.startGame(); },
    ]) assert.doesNotThrow(action);
    assert.equal(socket.sent.length, 0);
    assert.equal(f.app.pendingWildCard.id, 'wild', 'unsent color choices must remain available');
  }
});

test('rejoined players wait for authoritative state before acting', () => {
  const f = gameFixture();
  f.join();
  f.app.ws.sent = [];
  assert.equal(f.app.sendPlay('red', ''), false);
  f.app.drawCard(); f.app.catchUno('b');
  assert.equal(f.app.ws.sent.length, 0);
  f.app.handleMessage({
    type: 'state', hand: f.app.hand, discardTop: f.app.discardTop, topColor: 'red',
    gamePlayers: f.app.gamePlayers, yourTurn: true,
  });
  assert.equal(f.app.sendPlay('red', ''), true);
  assert.equal(f.app.ws.sent.at(-1).type, 'play');
});

test('fresh snapshots dismiss wild choices for expired turns or missing cards', () => {
  for (const yourTurn of [true, false]) {
    const f = gameFixture();
    f.app.pendingWildCard = f.app.hand[0];
    f.app.handleMessage({
      type: 'state', hand: yourTurn ? [] : f.app.hand, yourTurn,
      discardTop: f.app.discardTop, topColor: 'red', gamePlayers: f.app.gamePlayers,
    });
    assert.equal(f.app.pendingWildCard, null);
  }
});

test('an unchanged wild choice survives reconnect and sends only after synchronization', () => {
  const f = gameFixture();
  f.app.pendingWildCard = f.app.hand[0];
  f.join(); f.app.ws.sent = [];
  f.app.chooseColor('blue');
  assert.equal(f.app.pendingWildCard.id, 'wild');
  assert.equal(f.app.ws.sent.length, 0);
  f.app.handleMessage({
    type: 'state', hand: f.app.hand, yourTurn: true,
    discardTop: f.app.discardTop, topColor: 'red', gamePlayers: f.app.gamePlayers,
  });
  f.app.chooseColor('blue');
  assert.equal(f.app.ws.sent.at(-1).color, 'blue');
  assert.equal(f.app.pendingWildCard, null);
});

test('send failures trigger recovery without replaying the action', () => {
  const f = gameFixture();
  f.app.ws.send = () => { throw new Error('network failure'); };
  assert.equal(f.app.sendPlay('red', ''), false);
  assert.equal(f.app.ws, null);
  assert.equal(f.app.status, 'connecting');
  assert.ok(f.connection.retry);
  f.join();
  assert.equal(f.app.ws.sent.length, 1);
  assert.equal(f.app.ws.sent[0].type, 'hello');
});

test('pausing for another player preserves a valid wild choice without sending it', () => {
  const f = gameFixture();
  f.app.pendingWildCard = f.app.hand[0];
  f.app.ws.sent = [];
  f.app.handleMessage({
    type: 'state', hand: f.app.hand, yourTurn: true,
    discardTop: f.app.discardTop, topColor: 'red',
    gamePlayers: [{ id: 'a', connected: true }, { id: 'b', connected: false }],
  });
  f.app.chooseColor('blue');
  assert.equal(f.app.pendingWildCard.id, 'wild');
  assert.equal(f.app.ws.sent.length, 0);
});

test('legal actions still send on the synchronized connection', () => {
  const cases = [
    ['draw', app => app.drawCard()],
    ['play', app => app.sendPlay('red', '')],
    ['pass', app => { app.yourDrawnCard = app.hand[0]; app.keepDrawnCard(); }],
    ['acceptWild4', app => { app.canChallengeWild4 = true; app.acceptWildDrawFour(); }],
    ['challengeWild4', app => { app.canChallengeWild4 = true; app.challengeWildDrawFour(); }],
    ['callUno', app => app.callUno()],
    ['catchUno', app => app.catchUno('b')],
    ['start', app => { app.screen = 'lobby'; app.startGame(); }],
  ];
  for (const [type, action] of cases) {
    const f = gameFixture(); f.app.ws.sent = [];
    action(f.app);
    assert.equal(f.app.ws.sent.length, 1, type);
    assert.equal(f.app.ws.sent[0].type, type);
  }
});

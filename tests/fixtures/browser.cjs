const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

module.exports = function browser(options = {}) {
  let now = 1000;
  let nextTimer = 0;
  let makeApp;
  const timers = new Map();
  const listeners = {};
  const sockets = [];
  const requests = [];
  const storage = new Map(Object.entries(options.storage || {}));
  class Socket {
    static CONNECTING = 0;
    static OPEN = 1;
    constructor(url) {
      if (options.throwOnOpen) throw new Error('transport unavailable');
      this.url = url; this.readyState = 0; this.listeners = {}; this.sent = [];
      sockets.push(this);
    }
    addEventListener(event, fn) { this.listeners[event] = fn; }
    emit(event, data) { this.listeners[event]?.(data); }
    open() { this.readyState = 1; this.emit('open'); }
    message(data) { this.emit('message', { data: JSON.stringify(data) }); }
    send(data) {
      if (this.readyState !== Socket.OPEN) throw new Error('socket is not open');
      this.sent.push(JSON.parse(data));
    }
    close() { this.readyState = 3; this.emit('close'); }
  }
  const addTimer = (fn, delay, interval = false) => {
    const id = ++nextTimer;
    timers.set(id, { fn, delay, due: now + delay, interval });
    return id;
  };
  const events = scope => ({
    addEventListener: (name, fn) => {
      if (name === 'alpine:init') fn();
      else listeners[`${scope}:${name}`] = fn;
    },
    removeEventListener: (name, fn) => {
      if (listeners[`${scope}:${name}`] === fn) delete listeners[`${scope}:${name}`];
    },
  });
  const window = {
    innerWidth: 800,
    location: { protocol: 'https:', host: 'example.test', origin: 'https://example.test', pathname: options.pathname || '/' },
    ...events('window'),
  };
  const document = { visibilityState: 'visible', ...events('document') };
  const navigator = { onLine: options.online !== false };
  const context = vm.createContext({
    window, document, navigator, WebSocket: Socket, AbortController,
    fetch: (url, requestOptions) => new Promise(resolve => requests.push({ url, options: requestOptions, resolve })),
    Alpine: { data: (_, fn) => { makeApp = fn; } },
    Date: { now: () => now },
    setTimeout: (fn, delay) => addTimer(fn, delay),
    setInterval: (fn, delay) => addTimer(fn, delay, true),
    clearTimeout: id => timers.delete(id), clearInterval: id => timers.delete(id),
    localStorage: {
      getItem: key => storage.get(key) || null,
      setItem: (key, value) => storage.set(key, value),
      removeItem: key => storage.delete(key),
    },
    history: { pushState() {}, replaceState() {} },
  });
  function load(file) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../../web/js', file), 'utf8'), context);
  }
  load('connection.js');
  function app() {
    load('app.js');
    const app = makeApp();
    app.$nextTick = fn => fn(); app.$refs = {};
    for (const method of ['renderQrCode', 'prepareAudio', 'vibrate', 'playCardSfx', 'playUnoSfx', 'startMusic', 'stopMusic']) {
      app[method] = () => {};
    }
    app.init();
    return app;
  }
  function advance(ms) {
    const target = now + ms;
    for (let count = 0; count < 10000; count++) {
      const next = [...timers].filter(([, timer]) => timer.due <= target).sort((a, b) => a[1].due - b[1].due)[0];
      if (!next) { now = target; return; }
      const [id, timer] = next;
      now = timer.due;
      if (timer.interval) timer.due += timer.delay;
      else timers.delete(id);
      timer.fn();
    }
    throw new Error('timer loop did not settle');
  }
  return { connection: window.unoConnection, app, sockets, requests, timers, storage,
    window, document, navigator, listeners, advance,
    elapse: ms => { now += ms; },
    event: (scope, name) => listeners[`${scope}:${name}`]?.(),
  };
};

// Start the transport before the UI, styles, or service-worker update work.
// This controller owns every socket and timer; the UI only submits intent
// and consumes server snapshots. Retired sockets can never change its state.
class UnoConnection {
  constructor() {
    this.ws = null;
    this.hello = null;
    this.phase = 'idle';
    this.attempts = 0;
    this.deadline = null;
    this.deadlineAt = 0;
    this.retry = null;
    this.heartbeat = null;
    this.validation = null;
    this.lastMessageAt = 0;
    this.paused = false;
    this.backgrounded = document.visibilityState === 'hidden';
    this.disposed = false;
    this.pending = [];
    this.onChange = null;
    this.onMessage = null;

    const room = window.location.pathname.match(/^\/r\/([A-Za-z0-9]{4,8})$/)?.[1].toUpperCase();
    try {
      const identity = room && JSON.parse(localStorage.getItem(`uno:player:${room}`));
      if (typeof identity?.token === 'string' && identity.token && typeof identity.name === 'string' && identity.name.trim()) {
        this.hello = { type: 'hello', name: identity.name, room, create: false, token: identity.token };
      }
    } catch {
      // Storage is optional. A new player can still use the warm transport.
    }

    this.recover = () => {
      if (this.disposed || navigator.onLine === false || document.visibilityState === 'hidden') return;
      this.paused = false;
      const stale = (this.deadline !== null && Date.now() >= this.deadlineAt) ||
        (this.ws?.readyState === WebSocket.OPEN &&
          (this.backgrounded || Date.now() - this.lastMessageAt >= 30000));
      this.backgrounded = false;
      if (stale) this.retire();
      if (this.ws) return; // The first event already started a handshake.
      clearTimeout(this.retry);
      this.retry = null;
      this.attempts = 0;
      this.open();
    };
    this.suspend = () => {
      this.paused = true;
      clearTimeout(this.retry);
      this.retry = null;
      this.cancelValidation();
      this.retire();
      this.phase = 'paused';
      this.publish();
    };
    this.visibility = () => {
      if (document.visibilityState === 'hidden') this.backgrounded = true;
      else this.recover();
    };
    this.events = [
      [window, 'online', this.recover], [window, 'offline', this.suspend],
      [window, 'pageshow', this.recover], [window, 'pagehide', this.suspend],
      [document, 'resume', this.recover], [document, 'freeze', this.suspend],
      [document, 'visibilitychange', this.visibility],
    ];
    for (const [target, event, callback] of this.events) target.addEventListener(event, callback);
    this.open();
  }

  subscribe(onChange, onMessage) {
    this.onChange = onChange;
    this.onMessage = onMessage;
    this.publish();
    for (const message of this.pending.splice(0)) onMessage(message);
  }

  publish() {
    this.onChange?.({
      ws: this.ws,
      status: this.hello && this.phase !== 'ready' ? 'connecting' : 'ready',
      ready: this.phase === 'ready',
      attempts: this.attempts,
    });
  }

  deliver(message) {
    if (this.onMessage) this.onMessage(message);
    else this.pending.push(message);
  }

  join(hello) {
    // Repeated submit/lifecycle events must not abort a pending handshake.
    if (this.hello && JSON.stringify(this.hello) === JSON.stringify(hello) && this.ws) return;
    if (this.hello) this.retire();
    this.cancelValidation();
    clearTimeout(this.retry);
    this.retry = null;
    this.hello = { ...hello };
    this.attempts = 0;
    if (this.ws?.readyState === WebSocket.OPEN) this.sendHello(this.ws);
    else if (!this.ws) this.open();
    this.publish();
  }

  open() {
    if (this.ws || this.disposed || this.paused || navigator.onLine === false) return;
    this.phase = 'opening';
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    let ws;
    try {
      ws = new WebSocket(`${protocol}//${window.location.host}/ws`);
    } catch {
      this.failed(null);
      return;
    }
    this.ws = ws;
    this.armDeadline(ws, 2000);
    this.publish();
    ws.addEventListener('open', () => {
      if (this.ws !== ws) return;
      clearTimeout(this.deadline);
      this.deadline = null;
      this.lastMessageAt = Date.now();
      this.heartbeat = setInterval(() => {
        if (this.ws !== ws) return;
        if (Date.now() - this.lastMessageAt >= 30000) this.failed(ws);
        else this.write(ws, { type: 'ping' });
      }, 10000);
      if (this.hello) this.sendHello(ws);
      else {
        this.phase = 'idle';
        this.publish();
      }
    });
    ws.addEventListener('message', event => {
      if (this.ws !== ws) return;
      let message;
      try { message = JSON.parse(event.data); } catch { this.failed(ws); return; }
      if (!message || typeof message.type !== 'string') { this.failed(ws); return; }
      this.lastMessageAt = Date.now();
      if (message.type === 'heartbeat' || message.type === 'pong') return;
      if (message.type === 'error' && this.phase === 'joining') {
        this.reset();
        this.deliver(message);
        return;
      }
      if (message.type === 'joined') {
        this.hello = { type: 'hello', name: this.hello.name, room: message.roomId,
          create: false, token: message.token };
        this.cancelValidation();
      }
      if (message.type === 'started') {
        this.phase = 'joining';
        this.armDeadline(ws, 2000);
        this.publish();
      }
      this.deliver(message);
      if (message.type === 'state' || (message.type === 'joined' && message.roomStatus !== 'playing')) {
        clearTimeout(this.deadline);
        this.deadline = null;
        this.cancelValidation();
        this.phase = 'ready';
        this.attempts = 0;
        this.publish();
      }
    });
    ws.addEventListener('error', () => this.failed(ws));
    ws.addEventListener('close', () => this.failed(ws));
  }

  armDeadline(ws, milliseconds) {
    clearTimeout(this.deadline);
    this.deadlineAt = Date.now() + milliseconds;
    this.deadline = setTimeout(() => this.failed(ws), milliseconds);
  }

  sendHello(ws) {
    this.phase = 'joining';
    this.armDeadline(ws, 2000);
    this.publish();
    this.write(ws, this.hello);
  }

  write(ws, message) {
    try {
      ws.send(JSON.stringify(message));
      return true;
    } catch {
      this.failed(ws);
      return false;
    }
  }

  send(message) {
    if (this.phase !== 'ready' || this.ws?.readyState !== WebSocket.OPEN) return false;
    return this.write(this.ws, message);
  }

  retire() {
    const ws = this.ws;
    this.ws = null;
    this.pending = [];
    clearTimeout(this.deadline);
    clearInterval(this.heartbeat);
    this.deadline = this.heartbeat = null;
    if (ws) ws.close();
  }

  failed(ws) {
    if (this.ws !== ws || this.disposed) return;
    this.retire();
    this.phase = 'waiting';
    if (!this.paused && navigator.onLine !== false && this.retry === null) {
      const delay = Math.min(200 * 2 ** Math.min(this.attempts, 6), 10000) * (0.75 + Math.random() * 0.5);
      this.attempts++;
      this.retry = setTimeout(() => {
        this.retry = null;
        this.open();
      }, delay);
      this.validateSession(); // Independent of the retry timer and handshake.
    }
    this.publish();
  }

  cancelValidation() {
    if (!this.validation) return;
    this.validation.controller.abort();
    clearTimeout(this.validation.timer);
    this.validation = null;
  }

  async validateSession() {
    const hello = this.hello;
    if (!hello?.room || !hello.token || this.validation) return;
    const controller = new AbortController();
    const validation = { controller, timer: setTimeout(() => this.cancelValidation(), 4000) };
    this.validation = validation;
    try {
      const response = await fetch(`/api/session/${encodeURIComponent(hello.room)}`, {
        cache: 'no-store', signal: controller.signal,
        headers: { Authorization: `Bearer ${hello.token}` },
      });
      if (this.validation !== validation || this.hello !== hello) return;
      if (response.status === 404 || response.status === 401) {
        this.reset();
        this.deliver({ type: 'error', code: response.status === 404 ? 'room_not_found' : 'session_invalid',
          message: response.status === 404 ? 'That room no longer exists. Create a new room or join one with a code.' :
            'Your session has expired. Join the room again.' });
      }
    } catch {
      // HTTP cannot hold up socket recovery.
    } finally {
      if (this.validation === validation) this.cancelValidation();
    }
  }

  reset() {
    this.cancelValidation();
    clearTimeout(this.retry);
    this.retry = null;
    this.retire();
    this.hello = null;
    this.phase = 'idle';
    this.attempts = 0;
    this.publish();
  }

  destroy() {
    this.disposed = true;
    this.reset();
    for (const [target, event, callback] of this.events) target.removeEventListener(event, callback);
    this.onChange = this.onMessage = null;
  }
}

window.unoConnection = new UnoConnection();

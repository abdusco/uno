/**
 * @typedef {Object} Player
 * @property {string} id
 * @property {string} name
 * @property {boolean} isHost
 */

/**
 * @typedef {Object} HelloMsg
 * @property {'hello'} type
 * @property {string} name
 * @property {string} [room]
 * @property {boolean} create
 * @property {string} [roomName]
 */

/**
 * @typedef {Object} ServerMsg
 * @property {'joined'|'players'|'started'|'error'} type
 * @property {string} [roomId]
 * @property {Player} [self]
 * @property {Player[]} [players]
 * @property {string} [message]
 */

document.addEventListener('alpine:init', () => {
  Alpine.data('unoParty', () => ({
    // 'connecting' while a socket is being negotiated, 'ready' once open,
    // 'disconnected' if it drops.
    status: 'ready',

    // 'name' -> 'lobby' -> 'game'
    screen: 'name',

    name: '',
    joinCode: '',
    joiningRoom: null, // room code parsed from the URL, e.g. "ABCDE"

    /** @type {WebSocket|null} */
    ws: null,
    roomId: '',
    selfId: '',
    isHost: false,
    /** @type {Player[]} */
    players: [],
    shareLink: '',
    copied: false,
    errorMsg: '',

    /** @returns {void} */
    init() {
      const match = window.location.pathname.match(/^\/r\/([A-Za-z0-9]{4,8})$/);
      if (match) {
        this.joiningRoom = match[1].toUpperCase();
      }
      this.registerServiceWorker();
      this.$nextTick(() => this.$refs.nameInput && this.$refs.nameInput.focus());
    },

    /** @returns {void} */
    registerServiceWorker() {
      if ('serviceWorker' in navigator) {
        navigator.serviceWorker.register('/sw.js').catch(() => {
          // Non-fatal - app still works without offline support.
        });
      }
    },

    /** @returns {void} */
    submitName() {
      if (!this.name.trim()) return;
      this.errorMsg = '';
      if (this.joiningRoom) {
        this.connect({ type: 'hello', name: this.name.trim(), room: this.joiningRoom, create: false });
      } else {
        this.connect({ type: 'hello', name: this.name.trim(), create: true });
      }
    },

    /** @returns {void} */
    submitJoinCode() {
      const code = this.joinCode.trim().toUpperCase();
      if (!this.name.trim() || !code) return;
      this.errorMsg = '';
      this.connect({ type: 'hello', name: this.name.trim(), room: code, create: false });
    },

    /**
     * @param {HelloMsg} helloMsg
     * @returns {void}
     */
    connect(helloMsg) {
      this.status = 'connecting';
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
      this.ws = new WebSocket(`${proto}://${window.location.host}/ws`);

      this.ws.addEventListener('open', () => {
        this.status = 'ready';
        this.ws.send(JSON.stringify(helloMsg));
      });

      this.ws.addEventListener('message', (event) => {
        this.handleMessage(JSON.parse(event.data));
      });

      this.ws.addEventListener('close', () => {
        if (this.screen !== 'name') {
          this.status = 'disconnected';
        } else {
          this.status = 'ready';
        }
      });

      this.ws.addEventListener('error', () => {
        this.errorMsg = 'Could not connect. Please try again.';
        this.status = 'ready';
      });
    },

    /**
     * @param {ServerMsg} msg
     * @returns {void}
     */
    handleMessage(msg) {
      switch (msg.type) {
        case 'joined':
          this.roomId = msg.roomId;
          this.selfId = msg.self.id;
          this.isHost = msg.self.isHost;
          this.players = msg.players || [];
          this.shareLink = `${window.location.origin}/r/${this.roomId}`;
          history.pushState({}, '', `/r/${this.roomId}`);
          this.screen = 'lobby';
          break;
        case 'players':
          this.players = msg.players || [];
          // keep isHost in sync in case the original host disconnected
          // and we got promoted.
          const me = this.players.find(p => p.id === this.selfId);
          if (me) this.isHost = me.isHost;
          break;
        case 'started':
          this.screen = 'game';
          break;
        case 'error':
          this.errorMsg = msg.message || 'Something went wrong.';
          break;
      }
    },

    /** @returns {void} */
    startGame() {
      if (!this.ws) return;
      this.ws.send(JSON.stringify({ type: 'start' }));
    },

    /** @returns {void} */
    copyLink() {
      navigator.clipboard.writeText(this.shareLink).then(() => {
        this.copied = true;
        setTimeout(() => (this.copied = false), 1500);
      });
    },
  }));
});

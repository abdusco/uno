/**
 * @typedef {Object} Player
 * @property {string} id
 * @property {string} name
 * @property {boolean} isHost
 */

/**
 * @typedef {Object} Card
 * @property {string} id
 * @property {string} color - "red"|"yellow"|"green"|"blue"|"wild"
 * @property {string} value - "0".."9"|"skip"|"reverse"|"draw2"|"wild"|"wild4"
 */

/**
 * @typedef {Object} GamePlayer
 * @property {string} id
 * @property {string} name
 * @property {number} handCount
 * @property {boolean} isCurrentTurn
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
 * @property {'joined'|'players'|'started'|'state'|'gameOver'|'error'} type
 * @property {string} [roomId]
 * @property {Player} [self]
 * @property {Player[]} [players]
 * @property {string} [message]
 * @property {Card[]} [hand]
 * @property {Card} [discardTop]
 * @property {string} [topColor]
 * @property {GamePlayer[]} [gamePlayers]
 * @property {string} [currentPlayerId]
 * @property {boolean} [yourTurn]
 * @property {number} [deckCount]
 * @property {string[]} [log]
 * @property {Card} [yourDrawnCard]
 * @property {string} [winnerId]
 * @property {string} [winnerName]
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

    // --- in-game state, populated by "state" messages ---
    /** @type {Card[]} */
    hand: [],
    /** @type {Card|null} */
    discardTop: null,
    topColor: '',
    /** @type {GamePlayer[]} */
    gamePlayers: [],
    currentPlayerId: '',
    yourTurn: false,
    deckCount: 0,
    /** @type {string[]} */
    log: [],

    // wild card awaiting a color choice before it's sent to the server.
    /** @type {Card|null} */
    pendingWildCard: null,

    // set by the server right after this player draws a card that's
    // actually playable - shows the "play it or keep it" mini-prompt.
    /** @type {Card|null} */
    yourDrawnCard: null,

    // set once the round ends; cleared when everyone goes back to the lobby.
    /** @type {{winnerId: string, winnerName: string}|null} */
    gameOver: null,

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
          this.gameOver = null;
          this.screen = 'game';
          break;
        case 'state':
          this.hand = msg.hand || [];
          this.discardTop = msg.discardTop || null;
          this.topColor = msg.topColor || '';
          this.gamePlayers = msg.gamePlayers || [];
          this.currentPlayerId = msg.currentPlayerId || '';
          this.yourTurn = !!msg.yourTurn;
          this.deckCount = msg.deckCount || 0;
          this.log = msg.log || [];
          this.yourDrawnCard = msg.yourDrawnCard || null;
          break;
        case 'gameOver':
          this.gameOver = { winnerId: msg.winnerId, winnerName: msg.winnerName };
          this.pendingWildCard = null;
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

    /**
     * Whether `card` could legally be played on the current discard pile.
     * Mirrors gameState.isPlayable in game.go, purely for disabling cards
     * client-side - the server re-validates regardless.
     * @param {Card} card
     * @returns {boolean}
     */
    isPlayable(card) {
      if (!this.discardTop) return false;
      if (card.color === 'wild') return true;
      return card.color === this.topColor || card.value === this.discardTop.value;
    },

    /**
     * @param {Card} card
     * @returns {string} a full display label, e.g. "Red 7" or "Wild Draw Four"
     */
    cardLabel(card) {
      const names = {
        skip: 'Skip', reverse: 'Reverse', draw2: 'Draw Two',
        wild: 'Wild', wild4: 'Wild Draw Four', colorbomb: 'Color Bomb',
      };
      const valueLabel = names[card.value] || card.value;
      if (card.color === 'wild') return valueLabel;
      return `${card.color[0].toUpperCase()}${card.color.slice(1)} ${valueLabel}`;
    },

    /**
     * @param {Card} card
     * @returns {string} the short glyph shown on the card face, e.g. "7", "⇄", "+4"
     */
    cardGlyph(card) {
      const glyphs = { skip: '⦸', reverse: '⇄', draw2: '+2', wild: '★', wild4: '+4', colorbomb: '🎨' };
      return glyphs[card.value] || card.value;
    },

    /**
     * Full class string for a card button in the hand tray. Built as one
     * string rather than an Alpine `:class="[..., {cond: x}]"` array/object
     * mix - Alpine only special-cases a plain object or a plain array for
     * :class, not one nested inside the other, so a mixed array silently
     * stringifies the object to "[object Object]" instead of merging it.
     * @param {Card} card
     * @returns {string}
     */
    cardClass(card) {
      let cls = `uno-card--${card.color}`;
      if (!this.yourTurn || !this.isPlayable(card)) cls += ' uno-card--disabled';
      return cls;
    },

    /**
     * Called when the player clicks a card in their hand. Wild cards need a
     * color choice first, so those open the picker instead of playing
     * immediately.
     * @param {Card} card
     * @returns {void}
     */
    playCard(card) {
      if (!this.yourTurn || !this.isPlayable(card)) return;
      if (card.color === 'wild') {
        this.pendingWildCard = card;
        return;
      }
      this.sendPlay(card.id, '');
    },

    /**
     * @param {string} color
     * @returns {void}
     */
    chooseColor(color) {
      if (!this.pendingWildCard) return;
      this.sendPlay(this.pendingWildCard.id, color);
      this.pendingWildCard = null;
    },

    /** @returns {void} */
    cancelWildPick() {
      this.pendingWildCard = null;
    },

    /**
     * @param {string} cardId
     * @param {string} color
     * @returns {void}
     */
    sendPlay(cardId, color) {
      if (!this.ws) return;
      this.ws.send(JSON.stringify({ type: 'play', cardId, color }));
    },

    /** @returns {void} */
    drawCard() {
      if (!this.ws || !this.yourTurn || this.yourDrawnCard) return;
      this.ws.send(JSON.stringify({ type: 'draw' }));
    },

    /**
     * Plays the card just drawn (shown in the mini "play it or keep it"
     * prompt). Wild cards still need a color choice first.
     * @returns {void}
     */
    playDrawnCard() {
      if (!this.yourDrawnCard) return;
      const card = this.yourDrawnCard;
      if (card.color === 'wild') {
        this.pendingWildCard = card;
        return;
      }
      this.sendPlay(card.id, '');
    },

    /** @returns {void} */
    keepDrawnCard() {
      if (!this.ws) return;
      this.ws.send(JSON.stringify({ type: 'pass' }));
    },

    /**
     * Whether the "UNO!" button should be shown - once a player is down to
     * two cards they're allowed to call preemptively, right up until the
     * server clears the flag again on their next play.
     * @returns {boolean}
     */
    canCallUno() {
      return this.hand.length <= 2 && this.hand.length > 0 && !this.myUnoCalled();
    },

    /** @returns {boolean} */
    myUnoCalled() {
      const me = this.gamePlayers.find(p => p.id === this.selfId);
      return !!(me && me.unoCalled);
    },

    /** @returns {void} */
    callUno() {
      if (!this.ws) return;
      this.ws.send(JSON.stringify({ type: 'callUno' }));
    },

    /**
     * @param {GamePlayer} p
     * @returns {boolean} true if `p` can be caught out for not calling UNO
     */
    isCatchable(p) {
      return p.id !== this.selfId && p.handCount === 1 && !p.unoCalled;
    },

    /**
     * @param {string} targetId
     * @returns {void}
     */
    catchUno(targetId) {
      if (!this.ws) return;
      this.ws.send(JSON.stringify({ type: 'catchUno', targetId }));
    },

    /** @returns {void} */
    backToLobby() {
      this.gameOver = null;
      this.screen = 'lobby';
    },
  }));
});

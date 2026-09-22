// Must match the .uno-card width and normal gap in style.css - used to
// compute how many hand cards fit per row (see handCardGap()).
const CARD_WIDTH = 72;
const CARD_GAP = 8;
// Cards in a row never overlap by more than this fraction of their width
// before the hand splits into another row instead.
const MAX_CARD_OVERLAP = 0.3;
// Rough horizontal padding budget (both sides combined) around the hand
// row, subtracted from the viewport width to get available card-row width.
const HAND_SIDE_PADDING = 24;

/**
 * @typedef {Object} Player
 * @property {string} id
 * @property {string} name
 * @property {boolean} isHost
 * @property {boolean} connected
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
 * @property {boolean} connected
 */

/**
 * @typedef {Object} HelloMsg
 * @property {'hello'} type
 * @property {string} name
 * @property {string} [room]
 * @property {boolean} create
 * @property {string} [roomName]
 * @property {string} [token] - cached from a previous "joined", lets the
 *   server resume that identity instead of treating this as a new player.
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
 * @property {string} [token] - only on "joined"; cache it for next time.
 * @property {boolean} [resumed] - only on "joined"; true if this reconnected
 *   an existing player rather than creating a new one.
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
    // pre-rendered <svg> markup for the share link's QR code, injected via
    // x-html; rebuilt whenever shareLink changes (see renderQrCode()).
    qrSvg: '',
    copied: false,
    errorMsg: '',

    // --- reconnection ---
    // the very first hello sent this session, kept as a fallback for
    // reconnecting before we've ever successfully joined a room.
    /** @type {HelloMsg|null} */
    _firstHello: null,
    reconnectAttempts: 0,
    /** @type {number|null} */
    _reconnectTimer: null,
    // opaque secret handed back on "joined" - cached in localStorage
    // (keyed by room code) so a dropped connection or a full page reload
    // can resume this same identity instead of joining as someone new.
    token: '',
    // true right after a "joined" that resumed an existing player, for a
    // brief "reconnected" toast; cleared automatically.
    justResumed: false,

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

    // tracked reactively so handCardGap() re-runs on resize - e.g. a
    // phone rotating, or a desktop window being narrowed.
    viewportWidth: window.innerWidth,

    // Background music is synthesized locally with the Web Audio API. It
    // avoids shipping a large audio file and begins only after a user action,
    // which also respects browser autoplay rules.
    musicEnabled: true,
    /** @type {AudioContext|null} */
    audioContext: null,
    /** @type {number|null} */
    musicTimer: null,
    musicStep: 0,
    lastDiscardCardId: '',

    /** @returns {void} */
    init() {
      window.addEventListener('resize', () => {
        this.viewportWidth = window.innerWidth;
      });
      this.registerServiceWorker();
      try {
        this.musicEnabled = localStorage.getItem('uno:music') !== 'off';
      } catch {
        // Keep the default on if storage is unavailable.
      }
      const match = window.location.pathname.match(/^\/r\/([A-Za-z0-9]{4,8})$/);
      if (match) {
        this.joiningRoom = match[1].toUpperCase();
        const cached = this.loadCachedIdentity(this.joiningRoom);
        if (cached) {
          this.name = cached.name;
          this.connect({ type: 'hello', name: cached.name, room: this.joiningRoom, create: false, token: cached.token });
          return;
        }
      }
      this.$nextTick(() => this.$refs.nameInput && this.$refs.nameInput.focus());
    },

    /**
     * @param {string} roomCode
     * @returns {{token: string, name: string}|null}
     */
    loadCachedIdentity(roomCode) {
      try {
        const raw = localStorage.getItem(`uno:player:${roomCode}`);
        return raw ? JSON.parse(raw) : null;
      } catch {
        return null; // private browsing, storage disabled, corrupt JSON, etc.
      }
    },

    /**
     * @param {string} roomCode
     * @param {{token: string, name: string}} identity
     * @returns {void}
     */
    cacheIdentity(roomCode, identity) {
      try {
        localStorage.setItem(`uno:player:${roomCode}`, JSON.stringify(identity));
      } catch {
        // non-fatal - just means a reload won't auto-resume this time.
      }
    },

    /**
     * @param {string} roomCode
     * @returns {void}
     */
    clearCachedIdentity(roomCode) {
      try {
        localStorage.removeItem(`uno:player:${roomCode}`);
      } catch {
        // nothing to clean up if storage isn't available anyway.
      }
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
      if (!this._firstHello) this._firstHello = helloMsg;
      if (this._reconnectTimer) {
        clearTimeout(this._reconnectTimer);
        this._reconnectTimer = null;
      }

      this.status = 'connecting';
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
      // Captured by reference in every listener below, so a stale socket
      // from a superseded connection attempt can tell it's been replaced
      // and no-op instead of acting - without this guard, an old socket's
      // *own* eventual 'close' (it's still a live object even once
      // abandoned; nothing unsubscribes its listeners) fires
      // scheduleReconnect() again even after a newer connection already
      // succeeded, which reconnects, gets kicked because that same-token
      // takeover naturally displaces this tab's own newest connection,
      // fires *that* socket's 'close' too, and so on - an unbounded
      // reconnect ping-pong with nothing ever wrong on the network.
      const ws = new WebSocket(`${proto}://${window.location.host}/ws`);
      this.ws = ws;

      ws.addEventListener('open', () => {
        if (this.ws !== ws) return;
        this.status = 'ready';
        this.reconnectAttempts = 0;
        ws.send(JSON.stringify(helloMsg));
      });

      ws.addEventListener('message', (event) => {
        if (this.ws !== ws) return;
        this.handleMessage(JSON.parse(event.data));
      });

      ws.addEventListener('close', () => {
        if (this.ws !== ws) return;
        if (this.screen === 'name') {
          this.status = 'ready';
          return;
        }
        // Any drop past this point (mobile screen lock, wifi hiccup, a
        // laptop sleeping) gets retried automatically with backoff rather
        // than dumping the player onto a manual "reload" screen.
        this.scheduleReconnect();
      });

      ws.addEventListener('error', () => {
        if (this.ws !== ws) return;
        if (this.screen === 'name') {
          this.errorMsg = 'Could not connect. Please try again.';
          this.status = 'ready';
        }
        // otherwise: the 'close' event that follows schedules a reconnect
      });
    },

    /** @returns {void} */
    scheduleReconnect() {
      this.status = 'connecting';
      const delayMs = Math.min(1000 * 2 ** this.reconnectAttempts, 10000);
      this.reconnectAttempts++;
      this._reconnectTimer = setTimeout(() => {
        this.connect(this.buildReconnectHello());
      }, delayMs);
    },

    /**
     * Rejoining after a drop must always join-by-code, never re-create a
     * room - re-sending the original "create" hello would spin up a
     * second, empty room instead of rejoining the one already in play.
     * @returns {HelloMsg}
     */
    buildReconnectHello() {
      if (this.roomId) {
        return { type: 'hello', name: this.name.trim(), room: this.roomId, create: false, token: this.token };
      }
      return this._firstHello;
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
          this.renderQrCode();
          history.pushState({}, '', `/r/${this.roomId}`);
          this.token = msg.token || '';
          this.cacheIdentity(this.roomId, { token: this.token, name: this.name });
          // Don't clobber the game screen if this is a live-drop reconnect
          // mid-game - a personalized "state" is on its way right behind
          // this and will flip it back; only force the lobby screen when
          // there isn't already a game in progress to return to.
          if (!msg.resumed || this.screen !== 'game') {
            this.screen = 'lobby';
          }
          if (msg.resumed) {
            this.justResumed = true;
            setTimeout(() => (this.justResumed = false), 2000);
          }
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
          const previousDiscardId = this.lastDiscardCardId;
          const previousDeckCount = this.deckCount;
          this.hand = msg.hand || [];
          this.discardTop = msg.discardTop || null;
          this.topColor = msg.topColor || '';
          this.gamePlayers = msg.gamePlayers || [];
          this.currentPlayerId = msg.currentPlayerId || '';
          this.yourTurn = !!msg.yourTurn;
          this.deckCount = msg.deckCount || 0;
          this.log = msg.log || [];
          this.yourDrawnCard = msg.yourDrawnCard || null;
          this.lastDiscardCardId = this.discardTop ? this.discardTop.id : '';
          // Ignore the first state snapshot; after that, these differences
          // correspond to a card landing on the discard pile or leaving the
          // deck, regardless of which player made the move.
          if (previousDiscardId && this.lastDiscardCardId !== previousDiscardId) {
            this.playCardSfx('play');
          } else if (previousDeckCount && this.deckCount < previousDeckCount) {
            this.playCardSfx('draw');
          }
          // A resumed mid-game player gets here via "joined" + an
          // immediate personalized "state", never a fresh "started".
          this.screen = 'game';
          break;
        case 'gameOver':
          this.gameOver = { winnerId: msg.winnerId, winnerName: msg.winnerName };
          this.pendingWildCard = null;
          break;
        case 'error':
          this.errorMsg = msg.message || 'Something went wrong.';
          if (msg.message === 'room not found' && this.joiningRoom) this.resetMissingRoom();
          break;
      }
    },

    /**
     * Rooms are intentionally memory-only. A server restart therefore makes
     * old /r/CODE links and their saved reconnect tokens invalid. Clear that
     * local identity and return to the normal entry route instead of leaving
     * the player on a link that can never succeed.
     * @returns {void}
     */
    resetMissingRoom() {
      const missingRoom = this.joiningRoom;
      this.clearCachedIdentity(missingRoom);
      if (this.ws) {
        const staleSocket = this.ws;
        this.ws = null;
        staleSocket.close();
      }
      if (this._reconnectTimer) {
        clearTimeout(this._reconnectTimer);
        this._reconnectTimer = null;
      }
      this.joiningRoom = null;
      this.roomId = '';
      this.selfId = '';
      this.token = '';
      this._firstHello = null;
      this.status = 'ready';
      this.errorMsg = 'That room no longer exists. Create a new room or join one with a code.';
      history.replaceState({}, '', '/');
      this.$nextTick(() => this.$refs.nameInput && this.$refs.nameInput.focus());
    },

    /** @returns {void} */
    startGame() {
      if (!this.ws) return;
      this.startMusic();
      this.ws.send(JSON.stringify({ type: 'start' }));
    },

    /** @returns {void} */
    toggleMusic() {
      this.musicEnabled = !this.musicEnabled;
      try {
        localStorage.setItem('uno:music', this.musicEnabled ? 'on' : 'off');
      } catch {
        // The preference is optional; the current-session setting still works.
      }
      if (this.musicEnabled) this.startMusic();
      else this.stopMusic();
    },

    /** @returns {void} */
    startMusic() {
      if (!this.musicEnabled || this.musicTimer !== null) return;
      if (!this.prepareAudio()) return;
      this.playMusicStep();
      this.musicTimer = window.setInterval(() => this.playMusicStep(), 250);
    },

    /**
     * Create or resume the shared audio context. Call this directly from a
     * click-driven game action so later websocket-driven opponent effects can
     * play too without running afoul of autoplay restrictions.
     * @returns {boolean}
     */
    prepareAudio() {
      const AudioContextClass = window.AudioContext || window.webkitAudioContext;
      if (!AudioContextClass) return false;
      this.audioContext ||= new AudioContextClass();
      this.audioContext.resume().catch(() => {
        // A later user gesture will resume it if this one was too early.
      });
      return true;
    },

    /** @returns {void} */
    stopMusic() {
      if (this.musicTimer === null) return;
      clearInterval(this.musicTimer);
      this.musicTimer = null;
    },

    /** @returns {void} */
    playMusicStep() {
      if (!this.audioContext || this.audioContext.state !== 'running') return;
      // A tiny major-pentatonic arpeggio: bright enough for an arcade table,
      // deliberately quiet enough to sit behind conversation.
      const notes = [261.63, 329.63, 392, 523.25, 392, 329.63, 293.66, 392];
      const now = this.audioContext.currentTime;
      const oscillator = this.audioContext.createOscillator();
      const gain = this.audioContext.createGain();
      oscillator.type = this.musicStep % 8 === 0 ? 'triangle' : 'sine';
      oscillator.frequency.value = notes[this.musicStep % notes.length];
      gain.gain.setValueAtTime(0.0001, now);
      gain.gain.exponentialRampToValueAtTime(this.musicStep % 4 === 0 ? 0.026 : 0.014, now + 0.018);
      gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.22);
      oscillator.connect(gain).connect(this.audioContext.destination);
      oscillator.start(now);
      oscillator.stop(now + 0.23);
      this.musicStep++;
    },

    /**
     * @param {'play'|'draw'} effect
     * @returns {void}
     */
    playCardSfx(effect) {
      if (!this.audioContext || this.audioContext.state !== 'running') return;
      const now = this.audioContext.currentTime;
      const oscillator = this.audioContext.createOscillator();
      const gain = this.audioContext.createGain();
      const isPlay = effect === 'play';
      oscillator.type = isPlay ? 'triangle' : 'sine';
      oscillator.frequency.setValueAtTime(isPlay ? 620 : 310, now);
      oscillator.frequency.exponentialRampToValueAtTime(isPlay ? 220 : 560, now + .11);
      gain.gain.setValueAtTime(.0001, now);
      gain.gain.exponentialRampToValueAtTime(isPlay ? .042 : .028, now + .012);
      gain.gain.exponentialRampToValueAtTime(.0001, now + .13);
      oscillator.connect(gain).connect(this.audioContext.destination);
      oscillator.start(now);
      oscillator.stop(now + .14);
    },

    /**
     * Builds an <svg> QR code for shareLink (via the vendored qrcode.js -
     * see web/vendor/qrcode.js) and stashes it in qrSvg for x-html to
     * inject. Type 0 = let the library pick the smallest version that fits
     * the data; 'M' error correction is the library's usual default and
     * plenty for a short URL scanned from a phone at close range.
     * @returns {void}
     */
    renderQrCode() {
      if (!this.shareLink) {
        this.qrSvg = '';
        return;
      }
      const qr = qrcode(0, 'M');
      qr.addData(this.shareLink);
      qr.make();
      this.qrSvg = qr.createSvgTag({ cellSize: 5, margin: 0 });
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
      if (!this.yourTurn || this.waitingForReconnect() || !this.isPlayable(card)) cls += ' uno-card--disabled';
      return cls;
    },

    /**
     * A single per-card trailing gap (margin-right, in px - often negative,
     * i.e. an overlap) applied uniformly to every hand card, so the
     * browser's own `flex-wrap` does the actual row-breaking instead of
     * manually slicing the hand into row arrays. It's sized so that
     * exactly `perRow` cards - the equal-ish target row size for the
     * current hand length and viewport width - fit the available width,
     * using only as much overlap as that requires (often none) and never
     * more than 30% of a card's width. Applying it as trailing margin
     * (not leading) matters: a uniform *leading* margin would also yank
     * the first card of every wrapped row and the very first card of the
     * hand leftward; a trailing margin only ever pulls the *next* card on
     * the same line closer, so it's a no-op at both the hand's start and
     * every row-wrap boundary - exactly the "no gap needed there" cases.
     * @returns {number}
     */
    handCardGap() {
      const n = this.hand.length;
      if (n <= 1) return 0;
      const available = this.handRowWidth();
      const maxPerRow = this.maxCardsPerRow();
      const rows = Math.max(1, Math.ceil(n / maxPerRow));
      const perRow = Math.min(n, Math.ceil(n / rows));
      if (perRow <= 1) return 0;
      const natural = perRow * CARD_WIDTH + (perRow - 1) * CARD_GAP;
      if (natural <= available) return CARD_GAP;
      const advance = Math.max((available - CARD_WIDTH) / (perRow - 1), CARD_WIDTH * (1 - MAX_CARD_OVERLAP));
      return advance - CARD_WIDTH;
    },

    /**
     * How many cards fit in one row before needing more than 30% overlap
     * to do so, given the current viewport width.
     * @returns {number}
     */
    maxCardsPerRow() {
      const available = this.handRowWidth();
      const minAdvance = CARD_WIDTH * (1 - MAX_CARD_OVERLAP);
      return Math.max(1, Math.floor(1 + (available - CARD_WIDTH) / minAdvance));
    },

    /** @returns {number} usable width for a row of hand cards, in px */
    handRowWidth() {
      return Math.max(this.viewportWidth - HAND_SIDE_PADDING, CARD_WIDTH);
    },

    /**
     * Called when the player clicks a card in their hand. Wild cards need a
     * color choice first, so those open the picker instead of playing
     * immediately.
     * @param {Card} card
     * @returns {void}
     */
    playCard(card) {
      if (!this.yourTurn || this.waitingForReconnect() || !this.isPlayable(card)) return;
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
      this.prepareAudio();
      this.ws.send(JSON.stringify({ type: 'play', cardId, color }));
    },

    /** @returns {void} */
    drawCard() {
      if (!this.ws || !this.yourTurn || this.yourDrawnCard || this.waitingForReconnect()) return;
      this.prepareAudio();
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
     * True once fewer than two players in the game are still connected -
     * the server freezes the turn in this state rather than ending the
     * round, so the client mirrors that by blocking actions too.
     * @returns {boolean}
     */
    waitingForReconnect() {
      return this.gamePlayers.filter(p => p.connected).length < 2;
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

// One sixteenth-note in the background arrangement (about 83 BPM). The
// sequence spans 32 steps / two bars before repeating.
const MUSIC_STEP_SECONDS = 0.18;

// Four harmonically distinct chords underlying the background music: Cmaj7,
// Am7, Fmaj7 and G7. Each MUSIC_LEAD_PHRASES entry is a 32-step melody over
// these same four chords (8 steps each); one is picked at random every time
// the sequence loops back to step 0, so the loop doesn't play the identical
// melody every repeat.
const MUSIC_CHORDS = [
  [261.63, 329.63, 392, 493.88],
  [220, 261.63, 329.63, 392],
  [174.61, 220, 261.63, 329.63],
  [196, 246.94, 293.66, 349.23],
];
const MUSIC_BASS_ROOTS = [130.81, 110, 87.31, 98];
const MUSIC_LEAD_PHRASES = [
  [
    659.25, null, 783.99, 880, null, 783.99, 659.25, 587.33,
    523.25, null, 659.25, 783.99, 659.25, null, 587.33, null,
    523.25, 659.25, null, 698.46, 783.99, null, 698.46, 659.25,
    587.33, null, 659.25, 783.99, 880, 783.99, 698.46, 587.33,
  ],
  [
    880, 783.99, null, 659.25, 587.33, null, 659.25, 783.99,
    783.99, null, 659.25, 523.25, null, 587.33, 659.25, null,
    698.46, null, 783.99, 659.25, null, 523.25, 587.33, null,
    783.99, 659.25, null, 587.33, 523.25, null, 587.33, 659.25,
  ],
  [
    587.33, null, null, 659.25, null, 783.99, null, null,
    523.25, null, null, null, 587.33, null, 659.25, null,
    698.46, null, null, 783.99, null, null, 659.25, null,
    880, null, 783.99, null, 698.46, null, 587.33, null,
  ],
  // Dense, rest-free arpeggio run - closer to a chiptune/arcade lead.
  [
    523.25, 659.25, 783.99, 659.25, 523.25, 659.25, 783.99, 880,
    523.25, 659.25, 523.25, 587.33, 523.25, 659.25, 587.33, 523.25,
    587.33, 698.46, 783.99, 698.46, 587.33, 698.46, 783.99, 880,
    587.33, 698.46, 783.99, 880, 987.77, 880, 783.99, 698.46,
  ],
  // Octave-jumpy "power up" flourish, also arcade-flavored.
  [
    783.99, null, 880, 783.99, 659.25, null, 783.99, 880,
    659.25, 587.33, null, 659.25, 523.25, null, 587.33, 659.25,
    698.46, null, 783.99, 880, 783.99, null, 698.46, 783.99,
    880, 987.77, 880, 783.99, 698.46, 587.33, null, 880,
  ],
];

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
 * @property {boolean} unoCalled
 * @property {boolean} unoCatchable
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
 * @property {string} [code]
 * @property {Card[]} [hand]
 * @property {Card} [discardTop]
 * @property {string} [topColor]
 * @property {GamePlayer[]} [gamePlayers]
 * @property {string} [currentPlayerId]
 * @property {boolean} [yourTurn]
 * @property {number} [deckCount]
 * @property {string[]} [log]
 * @property {Card} [yourDrawnCard]
 * @property {boolean} [canChallengeWild4]
 * @property {string} [token] - only on "joined"; cache it for next time.
 * @property {boolean} [resumed] - only on "joined"; true if this reconnected
 *   an existing player rather than creating a new one.
 * @property {string} [winnerId]
 * @property {string} [winnerName]
 */

document.addEventListener('alpine:init', () => {
  Alpine.data('unoParty', () => ({
    // 'connecting' until authoritative room/game state, 'ready' once synced,
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

    reconnectAttempts: 0,
    connectionReady: false,
    _gameSynced: false,
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
    canChallengeWild4: false,

    // set once the round ends; cleared when everyone goes back to the lobby.
    /** @type {{winnerId: string, winnerName: string}|null} */
    gameOver: null,

    // Background music is synthesized locally with the Web Audio API. It
    // avoids shipping a large audio file and begins only after a user action,
    // which also respects browser autoplay rules.
    musicEnabled: true,
    /** @type {AudioContext|null} */
    audioContext: null,
    /** @type {number|null} */
    musicTimer: null,
    /** @type {GainNode|null} */
    musicGain: null,
    /** @type {number|null} */
    musicFadeTimer: null,
    musicStep: 0,
    // index into MUSIC_LEAD_PHRASES for the melody currently playing;
    // re-rolled each time the 32-step sequence loops back to its start.
    currentLeadPhrase: 0,
    lastDiscardCardId: '',

    /** @returns {void} */
    init() {
      this.registerServiceWorker();
      try {
        this.musicEnabled = localStorage.getItem('uno:music') !== 'off';
      } catch {
        // Keep the default on if storage is unavailable.
      }
      const match = window.location.pathname.match(/^\/r\/([A-Za-z0-9]{4,8})$/);
      if (match) {
        this.joiningRoom = match[1].toUpperCase();
        this.name = window.unoConnection.hello?.name || '';
      }
      window.unoConnection.subscribe(({ ws, status, ready, attempts }) => {
        this.ws = ws;
        this.status = status;
        this.connectionReady = ready;
        this.reconnectAttempts = attempts;
        if (status !== 'ready') this._gameSynced = false;
      }, message => this.handleMessage(message));
      this.$nextTick(() => this.$refs.nameInput && this.$refs.nameInput.focus());
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
        navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).catch(() => {
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

    /** @param {HelloMsg} helloMsg */
    connect(helloMsg) {
      window.unoConnection.join(helloMsg);
    },

    /** @returns {void} */
    destroy() {
      window.unoConnection.destroy();
      this.stopMusic();
    },

    /**
     * @param {ServerMsg} msg
     * @returns {void}
     */
    handleMessage(msg) {
      switch (msg.type) {
        case 'heartbeat':
        case 'pong':
          break;
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
          this._gameSynced = false;
          this.pendingWildCard = null;
          this.gameOver = null;
          this.errorMsg = '';
          this.canChallengeWild4 = false;
          this.screen = 'game';
          this.startMusic();
          break;
        case 'state':
          // State snapshots describe an active round. A reconnect can enter
          // a rematch without receiving its earlier "started" event.
          if (this.gameOver) {
            this.pendingWildCard = null;
            this.lastDiscardCardId = '';
            this.gamePlayers = [];
            this.deckCount = 0;
          }
          this.gameOver = null;
          // A state transition supersedes transient action errors. This is
          // especially important for simultaneous UNO catches: one catcher
          // may lose the race while the successful catch immediately
          // broadcasts the authoritative state.
          this.errorMsg = '';
          const wasYourTurn = this.yourTurn;
          const previousDiscardId = this.lastDiscardCardId;
          const previousDeckCount = this.deckCount;
          const previousGamePlayers = this.gamePlayers;
          this.hand = msg.hand || [];
          this.discardTop = msg.discardTop || null;
          this.topColor = msg.topColor || '';
          this.gamePlayers = msg.gamePlayers || [];
          this.currentPlayerId = msg.currentPlayerId || '';
          this.yourTurn = !!msg.yourTurn;
          if (!wasYourTurn && this.yourTurn) this.vibrate();
          this.deckCount = msg.deckCount || 0;
          this.log = msg.log || [];
          this.yourDrawnCard = msg.yourDrawnCard || null;
          this.canChallengeWild4 = !!msg.canChallengeWild4;
          this._gameSynced = true;
          if (this.pendingWildCard && (!this.yourTurn || this.canChallengeWild4 ||
            !this.hand.some(card => card.id === this.pendingWildCard.id) ||
            (this.yourDrawnCard && this.yourDrawnCard.id !== this.pendingWildCard.id))) {
            this.pendingWildCard = null;
          }
          this.lastDiscardCardId = this.discardTop ? this.discardTop.id : '';
          // Ignore the first state snapshot; after that, these differences
          // correspond to a card landing on the discard pile or leaving the
          // deck, regardless of which player made the move.
          if (previousDiscardId && this.lastDiscardCardId !== previousDiscardId) {
            this.playCardSfx('play');
          } else if (previousDeckCount && this.deckCount < previousDeckCount) {
            this.playCardSfx('draw');
          }
          // UNO calls arrive as state transitions rather than standalone
          // events. Ignore the first snapshot after joining/reconnecting, then
          // sound the call when any player changes from not-called to called.
          if (previousGamePlayers.length && this.gamePlayers.some(player => {
            const previous = previousGamePlayers.find(candidate => candidate.id === player.id);
            return player.unoCalled && previous && !previous.unoCalled;
          })) {
            this.playUnoSfx();
          }
          // A resumed mid-game player gets here via "joined" + an
          // immediate personalized "state", never a fresh "started".
          this.screen = 'game';
          break;
        case 'gameOver':
          this.gameOver = { winnerId: msg.winnerId, winnerName: msg.winnerName };
          this.errorMsg = '';
          this.pendingWildCard = null;
          this.canChallengeWild4 = false;
          this.stopMusic(true);
          if (msg.winnerId === this.selfId) {
            this.playTriumphSfx();
          }
          break;
        case 'error':
          if (['room_not_found', 'session_invalid'].includes(msg.code)) this.resetMissingRoom();
          this.errorMsg = msg.message || 'Something went wrong.';
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
      const missingRoom = this.roomId || this.joiningRoom;
      this.clearCachedIdentity(missingRoom);
      window.unoConnection.reset();
      this.joiningRoom = null;
      this.roomId = '';
      this.selfId = '';
      this.token = '';
      this.reconnectAttempts = 0;
      this.status = 'ready';
      this.screen = 'name';
      this.players = [];
      this.gamePlayers = [];
      this.hand = [];
      this._gameSynced = false;
      this.canChallengeWild4 = false;
      this.gameOver = null;
      this.errorMsg = 'That room no longer exists. Create a new room or join one with a code.';
      history.replaceState({}, '', '/');
      this.$nextTick(() => this.$refs.nameInput && this.$refs.nameInput.focus());
    },

    /** @returns {void} */
    startGame() {
      if (!this.canSend() || !this.isHost || this.screen !== 'lobby') return;
      this.startMusic();
      this.sendAction({ type: 'start' });
    },

    /** @returns {boolean} */
    canSend() {
      return this.connectionReady && this.status === 'ready' && this.ws?.readyState === WebSocket.OPEN;
    },

    /** @returns {boolean} whether the current game can accept an action */
    get canAct() {
      return this.canSend() && this._gameSynced && this.screen === 'game' &&
        !this.gameOver && !this.waitingForReconnect();
    },

    /**
     * Actions use the accepted socket and, during play, its fresh snapshot.
     * Failed actions are never queued for replay against a later turn.
     * @param {Object} msg
     * @returns {boolean}
     */
    sendAction(msg) {
      if (!this.canSend()) return false;
      if (msg.type !== 'start' && !this.canAct) return false;
      return window.unoConnection.send(msg);
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
      if (this.musicFadeTimer !== null) {
        clearTimeout(this.musicFadeTimer);
        this.musicFadeTimer = null;
      }
      const now = this.audioContext.currentTime;
      this.musicGain.gain.cancelScheduledValues(now);
      this.musicGain.gain.setValueAtTime(Math.max(this.musicGain.gain.value, .0001), now);
      this.musicGain.gain.exponentialRampToValueAtTime(.72, now + .45);
      this.playMusicStep();
      this.musicTimer = window.setInterval(() => this.playMusicStep(), MUSIC_STEP_SECONDS * 1000);
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
      if (!this.musicGain) {
        this.musicGain = this.audioContext.createGain();
        this.musicGain.gain.value = .72;
        this.musicGain.connect(this.audioContext.destination);
      }
      this.audioContext.resume().catch(() => {
        // A later user gesture will resume it if this one was too early.
      });
      return true;
    },

    /**
     * @param {boolean} [fadeOut=false]
     * @returns {void}
     */
    stopMusic(fadeOut = false) {
      if (this.musicTimer !== null) {
        clearInterval(this.musicTimer);
        this.musicTimer = null;
      }
      if (!this.audioContext || !this.musicGain) return;
      if (this.musicFadeTimer !== null) {
        clearTimeout(this.musicFadeTimer);
        this.musicFadeTimer = null;
      }
      const now = this.audioContext.currentTime;
      this.musicGain.gain.cancelScheduledValues(now);
      this.musicGain.gain.setValueAtTime(Math.max(this.musicGain.gain.value, .0001), now);
      if (fadeOut) {
        this.musicGain.gain.exponentialRampToValueAtTime(.0001, now + 1.25);
        this.musicFadeTimer = window.setTimeout(() => {
          this.musicFadeTimer = null;
        }, 1300);
      } else {
        this.musicGain.gain.setValueAtTime(.0001, now);
      }
    },

    /** @returns {void} */
    playMusicStep() {
      if (!this.audioContext || !this.musicGain || this.audioContext.state !== 'running') return;
      const step = this.musicStep % 32;
      // Re-roll which melody plays over the next 32 steps, favoring a
      // different one than just finished so it's noticeable.
      if (step === 0 && MUSIC_LEAD_PHRASES.length > 1) {
        let next = this.currentLeadPhrase;
        while (next === this.currentLeadPhrase) {
          next = Math.floor(Math.random() * MUSIC_LEAD_PHRASES.length);
        }
        this.currentLeadPhrase = next;
      }
      const lead = MUSIC_LEAD_PHRASES[this.currentLeadPhrase];
      const chordIndex = Math.floor(step / 8);
      const chord = MUSIC_CHORDS[chordIndex];
      const now = this.audioContext.currentTime;

      this.scheduleMusicNote(chord[step % chord.length], now, .3, 'sine', .012);
      if (step % 4 === 0) {
        this.scheduleMusicNote(MUSIC_BASS_ROOTS[chordIndex], now, .58, 'triangle', .024);
      }
      if (lead[step]) {
        this.scheduleMusicNote(lead[step], now, step % 4 === 0 ? .28 : .18, 'triangle', .02);
      }
      if (step % 2 === 1) {
        this.scheduleMusicNote(step % 4 === 1 ? 1174.66 : 987.77, now, .035, 'square', .0035);
      }
      this.musicStep++;
    },

    /**
     * @param {number} frequency
     * @param {number} start
     * @param {number} duration
     * @param {OscillatorType} type
     * @param {number} volume
     * @returns {void}
     */
    scheduleMusicNote(frequency, start, duration, type, volume) {
      const oscillator = this.audioContext.createOscillator();
      const gain = this.audioContext.createGain();
      oscillator.type = type;
      oscillator.frequency.value = frequency;
      gain.gain.setValueAtTime(.0001, start);
      gain.gain.exponentialRampToValueAtTime(volume, start + .012);
      gain.gain.exponentialRampToValueAtTime(.0001, start + duration);
      oscillator.connect(gain).connect(this.musicGain);
      oscillator.start(start);
      oscillator.stop(start + duration + .01);
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

    /** @returns {void} */
    vibrate() {
      if (typeof navigator !== 'undefined' && typeof navigator.vibrate === 'function') {
        navigator.vibrate(100);
      }
    },

    /** @returns {void} */
    playUnoSfx() {
      if (!this.audioContext || this.audioContext.state !== 'running') return;
      const now = this.audioContext.currentTime;
      [523.25, 783.99].forEach((frequency, index) => {
        const oscillator = this.audioContext.createOscillator();
        const gain = this.audioContext.createGain();
        const start = now + index * .09;
        oscillator.type = 'square';
        oscillator.frequency.value = frequency;
        gain.gain.setValueAtTime(.0001, start);
        gain.gain.exponentialRampToValueAtTime(.045, start + .012);
        gain.gain.exponentialRampToValueAtTime(.0001, start + .12);
        oscillator.connect(gain).connect(this.audioContext.destination);
        oscillator.start(start);
        oscillator.stop(start + .13);
      });
    },

    /**
     * A quick ascending run building into a stacked landing chord, rather
     * than one plain rising arpeggio - the run-up (square wave, tight
     * spacing) creates anticipation that resolves into a fuller, longer-held
     * chord (triangle wave, slightly strummed) instead of a single note.
     * @returns {void}
     */
    playTriumphSfx() {
      if (!this.prepareAudio() || this.audioContext.state !== 'running') return;
      const now = this.audioContext.currentTime;
      const run = [523.25, 587.33, 659.25, 783.99, 880, 1046.5]; // C5 D5 E5 G5 A5 C6
      run.forEach((frequency, index) => {
        const oscillator = this.audioContext.createOscillator();
        const gain = this.audioContext.createGain();
        const start = now + index * .075;
        oscillator.type = 'square';
        oscillator.frequency.value = frequency;
        gain.gain.setValueAtTime(.0001, start);
        gain.gain.exponentialRampToValueAtTime(.05, start + .012);
        gain.gain.exponentialRampToValueAtTime(.0001, start + .16);
        oscillator.connect(gain).connect(this.audioContext.destination);
        oscillator.start(start);
        oscillator.stop(start + .17);
      });
      const chordStart = now + run.length * .075;
      const chord = [1046.5, 1318.51, 1567.98]; // C6 E6 G6
      chord.forEach((frequency, index) => {
        const oscillator = this.audioContext.createOscillator();
        const gain = this.audioContext.createGain();
        const start = chordStart + index * .02;
        oscillator.type = 'triangle';
        oscillator.frequency.value = frequency;
        gain.gain.setValueAtTime(.0001, start);
        gain.gain.exponentialRampToValueAtTime(.06, start + .02);
        gain.gain.exponentialRampToValueAtTime(.0001, start + .65);
        oscillator.connect(gain).connect(this.audioContext.destination);
        oscillator.start(start);
        oscillator.stop(start + .66);
      });
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
      if (!this.canPlayCard(card)) cls += ' uno-card--disabled';
      return cls;
    },

    /** @param {Card} card @returns {boolean} */
    canPlayCard(card) {
      return this.canAct && this.yourTurn && !this.canChallengeWild4 && this.isPlayable(card) &&
        this.hand.some(candidate => candidate.id === card.id) &&
        (!this.yourDrawnCard || this.yourDrawnCard.id === card.id);
    },

    /** @returns {number} up to two balanced rows; small hands stay in one */
    get handColumns() {
      return Math.max(1, this.hand.length > 4 ? Math.ceil(this.hand.length / 2) : this.hand.length);
    },

    /**
     * Each card spans two grid tracks. An odd hand offsets the shorter
     * second row by one track to center it without spacer elements.
     * @param {number} index
     * @returns {number}
     */
    handCardColumn(index) {
      const columns = this.handColumns;
      const offset = index >= columns && this.hand.length % 2 ? 1 : 0;
      return (index % columns) * 2 + 1 + offset;
    },

    /**
     * Called when the player clicks a card in their hand. Wild cards need a
     * color choice first, so those open the picker instead of playing
     * immediately.
     * @param {Card} card
     * @returns {void}
     */
    playCard(card) {
      if (!this.canPlayCard(card)) return;
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
      if (this.sendPlay(this.pendingWildCard.id, color)) this.pendingWildCard = null;
    },

    /** @returns {void} */
    cancelWildPick() {
      this.pendingWildCard = null;
    },

    /**
     * @param {string} cardId
     * @param {string} color
     * @returns {boolean}
     */
    sendPlay(cardId, color) {
      const card = this.hand.find(candidate => candidate.id === cardId);
      if (!card || !this.canPlayCard(card)) return false;
      this.prepareAudio();
      return this.sendAction({ type: 'play', cardId, color });
    },

    /** @returns {void} */
    drawCard() {
      if (!this.canAct || !this.yourTurn || this.yourDrawnCard || this.canChallengeWild4) return;
      this.prepareAudio();
      this.sendAction({ type: 'draw' });
    },

    /**
     * Plays the card just drawn (shown in the mini "play it or keep it"
     * prompt). Wild cards still need a color choice first.
     * @returns {void}
     */
    playDrawnCard() {
      if (this.yourDrawnCard) this.playCard(this.yourDrawnCard);
    },

    /** @returns {void} */
    keepDrawnCard() {
      if (!this.yourTurn || !this.yourDrawnCard) return;
      this.sendAction({ type: 'pass' });
    },

    /** @returns {void} */
    acceptWildDrawFour() {
      if (!this.canChallengeWild4) return;
      this.sendAction({ type: 'acceptWild4' });
    },

    /** @returns {void} */
    challengeWildDrawFour() {
      if (!this.canChallengeWild4) return;
      this.sendAction({ type: 'challengeWild4' });
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
      const me = this.gamePlayers.find(p => p.id === this.selfId);
      if (!me || me.unoCalled) return false;
      return (this.yourTurn && this.hand.length === 2) || (this.hand.length === 1 && me.unoCatchable);
    },

    /** @returns {boolean} */
    myUnoCalled() {
      const me = this.gamePlayers.find(p => p.id === this.selfId);
      return !!(me && me.unoCalled);
    },

    /** @returns {void} */
    callUno() {
      if (!this.canAct || !this.canCallUno()) return;
      this.prepareAudio();
      this.sendAction({ type: 'callUno' });
    },

    /**
     * @param {GamePlayer} p
     * @returns {boolean} true if `p` can be caught out for not calling UNO
     */
    isCatchable(p) {
      return p.id !== this.selfId && p.unoCatchable;
    },

    /**
     * @param {string} targetId
     * @returns {void}
     */
    catchUno(targetId) {
      const target = this.gamePlayers.find(player => player.id === targetId);
      if (this.gameOver || !target || !this.isCatchable(target)) return;
      this.sendAction({ type: 'catchUno', targetId });
    },

    /** @returns {string} */
    gameOverMessage() {
      if (!this.gameOver) return '';
      return this.gameOver.winnerId === this.selfId ? 'You win!' : `${this.gameOver.winnerName} wins!`;
    },

    /** @returns {void} */
    backToLobby() {
      this.gameOver = null;
      this.errorMsg = '';
      this.gamePlayers = [];
      this.hand = [];
      this.yourTurn = false;
      this.currentPlayerId = '';
      this.yourDrawnCard = null;
      this.canChallengeWild4 = false;
      this.screen = 'lobby';
    },
  }));
});

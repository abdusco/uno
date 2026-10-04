const CACHE_NAME = 'uno-party-v39';
const SHELL_ASSETS = [
  '/',
  '/css/style.css',
  '/js/app.js',
  '/js/connection.js',
  '/vendor/alpine.min.js',
  '/vendor/qrcode.js',
  '/manifest.json',
  '/uno.svg',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
  '/icons/apple-touch-icon.png',
];

/**
 * @param {ExtendableEvent} event
 * @returns {void}
 */
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) =>
      cache.addAll(SHELL_ASSETS.map(url => new Request(url, { cache: 'reload' })))
    )
  );
  self.skipWaiting();
});

/**
 * @param {ExtendableEvent} event
 * @returns {void}
 */
self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((keys) =>
        Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k)))
      ),
      self.clients.claim(),
    ])
  );
});

/**
 * @param {FetchEvent} event
 * @returns {void}
 */
self.addEventListener('fetch', (event) => {
  const { request } = event;

  const requestURL = new URL(request.url);
  if (requestURL.origin !== self.location.origin || !['http:', 'https:'].includes(requestURL.protocol)) return;

  // Never intercept the websocket handshake.
  if (request.url.startsWith('ws:') || request.url.startsWith('wss:')) return;
  if (request.method !== 'GET') return;

  // A version's HTML and scripts stay together. Room/session validation is
  // performed by the app; launching the installed PWA never waits on HTTP.
  if (request.mode === 'navigate') {
    event.respondWith(
      caches.open(CACHE_NAME).then(async (cache) =>
        (await cache.match('/')) || fetch(request)
      )
    );
    return;
  }

  // Only shell assets belong in this cache. Session checks always use HTTP.
  if (!SHELL_ASSETS.includes(requestURL.pathname)) return;
  event.respondWith(
    caches.open(CACHE_NAME).then(async (cache) => {
      const cached = await cache.match(request);
      if (cached) return cached;
      return fetch(request).then(async (response) => {
        if (response.ok) {
          const copy = response.clone();
          await cache.put(request, copy);
        }
        return response;
      });
    })
  );
});

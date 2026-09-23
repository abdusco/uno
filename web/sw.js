const CACHE_NAME = 'uno-party-v27';
const SHELL_ASSETS = [
  '/',
  '/index.html',
  '/css/style.css',
  '/js/app.js',
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
    caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL_ASSETS))
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

  // Always ask the server for page navigations first. It already maps room
  // URLs such as /r/ABCDE to the app shell, and using the network prevents a
  // missing or stale cache entry from turning a valid invite into ERR_FAILED.
  // The cached shell is only an offline fallback.
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request)
        .then(async (response) => {
          if (response.ok) {
            const copy = response.clone();
            const cache = await caches.open(CACHE_NAME);
            await cache.put('/index.html', copy);
          }
          return response;
        })
        .catch(async () => {
          const cached = await caches.match('/index.html');
          return cached || Response.error();
        })
    );
    return;
  }

  // Shell assets: cache-first, falling back to network.
  event.respondWith(
    caches.match(request).then((cached) => {
      if (cached) return cached;
      return fetch(request).then((response) => {
        if (response.ok) {
          const copy = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(request, copy));
        }
        return response;
      });
    })
  );
});

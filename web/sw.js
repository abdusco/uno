const CACHE_NAME = 'uno-party-v13';
const SHELL_ASSETS = [
  '/',
  '/index.html',
  '/css/style.css',
  '/js/app.js',
  '/vendor/alpine.min.js',
  '/vendor/qrcode.js',
  '/manifest.json',
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
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k)))
    )
  );
  self.clients.claim();
});

/**
 * @param {FetchEvent} event
 * @returns {void}
 */
self.addEventListener('fetch', (event) => {
  const { request } = event;

  // Never intercept the websocket handshake.
  if (request.url.startsWith('ws:') || request.url.startsWith('wss:')) return;
  if (request.method !== 'GET') return;

  const url = new URL(request.url);

  // Room links like /r/ABCDE aren't real files - always resolve them to
  // the cached app shell so client-side routing can take over.
  if (url.pathname.startsWith('/r/')) {
    event.respondWith(caches.match('/index.html'));
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

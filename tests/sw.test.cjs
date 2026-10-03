const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

test('installed shell starts from its versioned cache without a network request', async () => {
  const listeners = {};
  const shell = { body: 'cached shell' };
  const cacheNames = [];
  let networkCalls = 0;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/sw.js'), 'utf8'), {
    self: { location: { origin: 'https://example.test' }, addEventListener: (name, fn) => { listeners[name] = fn; } },
    URL,
    caches: { open: async name => { cacheNames.push(name); return { match: async key => key === '/index.html' ? shell : null }; } },
    fetch: () => { networkCalls++; throw new Error('network stalled'); },
  });
  for (const route of ['/', '/r/ABCDE']) {
    let response;
    listeners.fetch({ request: { url: `https://example.test${route}`, method: 'GET', mode: 'navigate' }, respondWith: value => { response = value; } });
    assert.equal(await response, shell);
  }
  assert.equal(networkCalls, 0);
  assert.ok(cacheNames.every(name => name === 'uno-party-v35'));
  let intercepted = false;
  listeners.fetch({ request: { url: 'https://example.test/api/session/ABCDE', method: 'GET' }, respondWith: () => { intercepted = true; } });
  assert.equal(intercepted, false, 'session validation bypasses the cache');
});

test('a shell update bypasses stale browser HTTP cache entries', async () => {
  const listeners = {};
  let assets;
  let installed;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../web/sw.js'), 'utf8'), {
    self: { addEventListener: (name, fn) => { listeners[name] = fn; }, skipWaiting() {} },
    Request: class { constructor(url, options) { this.url = url; this.cache = options.cache; } },
    caches: { open: async () => ({ addAll: async requests => { assets = requests; } }) },
  });
  listeners.install({ waitUntil: promise => { installed = promise; } });
  await installed;
  assert.ok(assets.some(request => request.url === '/js/app.js'));
  assert.ok(assets.every(request => request.cache === 'reload'));
});

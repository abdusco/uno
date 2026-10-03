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
  assert.ok(cacheNames.every(name => name === 'uno-party-v34'));
  let intercepted = false;
  listeners.fetch({ request: { url: 'https://example.test/api/session/ABCDE', method: 'GET' }, respondWith: () => { intercepted = true; } });
  assert.equal(intercepted, false, 'session validation bypasses the cache');
});

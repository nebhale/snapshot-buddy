const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const app = fs.readFileSync(`${__dirname}/../web/app.js`, 'utf8');
const source = fs.readFileSync(`${__dirname}/../web/updates.js`, 'utf8');
const settle = () => new Promise(resolve => setImmediate(resolve));
function browser({ status = 200, deferred = false } = {}) {
  const timers = new Map(), events = {}, windowEvents = {}, streams = [], pending = [];
  let timerID = 0, requests = 0, applied = 0;
  const indicator = { hidden: true, textContent: '' };
  const root = { dataset: { view: 'dashboard', instance: 'one' }, dispatchEvent() {} };
  const doc = { hidden: false, querySelector: () => null, querySelectorAll: () => [], getElementById: () => indicator,
    addEventListener(name, fn) { events[name] = fn; } };
  class EventSource {
    constructor() { this.events = {}; streams.push(this); }
    addEventListener(name, fn) { this.events[name] = fn; }
    close() { this.closed = true; }
    emit(name, data = 'one') { this.events[name]?.({ data }); }
  }
  const window = { EventSource, location: { search: '' }, addEventListener(name, fn) { windowEvents[name] = fn; } };
  const context = vm.createContext({ document: doc, window, EventSource, Intl, URLSearchParams, AbortController,
    CustomEvent: class {}, setTimeout(fn, delay) { const id = ++timerID; timers.set(id, { fn, delay }); return id; }, clearTimeout(id) { timers.delete(id); },
    fetch: async () => {
      requests++;
      if (deferred) await new Promise(resolve => pending.push(resolve));
      return { status, ok: status === 200, json: async () => ({ instance: 'one', csrf: 'token', regions: [{ id: 'summary' }] }) };
    },
  });
  vm.runInContext(app + '\n' + source, context);
  vm.runInContext('BuddyUpdates.prototype.apply = function() { applied(); }; BuddyUpdates.prototype.afterUpdate = function() {};', Object.assign(context, { applied() { applied++; } }));
  context.root = root;
  const updates = vm.runInContext('new BuddyUpdates(root)', context);
  return { updates, streams, timers, events, windowEvents, doc, indicator, pending, requests: () => requests, applied: () => applied };
}
test('SSE changes resynchronize without a page reload and heartbeats keep the stream healthy', async () => {
  const b = browser(); await settle();
  b.streams[0].emit('change'); await settle();
  assert.equal(b.indicator.textContent, 'Live'); assert.ok(b.applied() >= 2);
  b.streams[0].emit('heartbeat');
  assert.ok([...b.timers.values()].some(t => t.delay === 45000));
  assert.ok(![...b.timers.values()].some(t => t.delay === 3000));
});
test('stream failure starts polling and a new stream stops fallback polling', async () => {
  const b = browser(); await settle();
  b.streams[0].onerror(); await settle();
  assert.equal(b.streams[0].closed, true);
  assert.ok([...b.timers.values()].some(t => t.delay === 3000));
  const retry = [...b.timers.values()].find(t => t.delay === 1000); retry.fn();
  b.streams[1].emit('change'); await settle();
  assert.equal(b.indicator.textContent, 'Live');
  assert.ok(![...b.timers.values()].some(t => t.delay === 3000));
});
test('hidden tabs close streams and discard outstanding responses; visibility restores synchronization', async () => {
  const b = browser({ deferred: true });
  b.doc.hidden = true; b.events.visibilitychange();
  assert.equal(b.streams[0].closed, true);
  b.pending.shift()(); await settle(); assert.equal(b.applied(), 0);
  const count = b.requests(); await b.updates.refresh(); assert.equal(b.requests(), count);
  b.doc.hidden = false; b.events.visibilitychange();
  assert.equal(b.streams.length, 2);
  b.pending.shift()(); await settle(); assert.equal(b.applied(), 1);
});
test('expired authentication stops reconnection without reloading the page', async () => {
  const b = browser({ status: 401 }); await settle();
  assert.match(b.indicator.textContent, /Sign-in required/);
  assert.equal(b.streams[0].closed, true); assert.equal(b.timers.size, 0);
  const count = b.requests(); await b.updates.refresh(); assert.equal(b.requests(), count);
});
test('notifications during a fetch coalesce into one follow-up refresh', async () => {
  const b = browser({ deferred: true });
  for (let i = 0; i < 20; i++) b.streams[0].emit('change');
  assert.equal(b.requests(), 1);
  b.pending.shift()(); await settle(); assert.equal(b.requests(), 2);
  b.pending.shift()(); await settle(); assert.equal(b.requests(), 2);
});

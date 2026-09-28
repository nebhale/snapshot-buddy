const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");

const source = readFileSync(join(__dirname, "../web/app.js"), "utf8");
function browser(timeZone, recovered = false) {
  const time = { dateTime: "2026-01-01T01:02:03Z", textContent: "UTC fallback" };
  const heading = { get textContent() { return `Recovered print — ${time.textContent}`; } };
  const document = {
    title: "Original title",
    querySelectorAll(selector) { return selector === "time[data-local-time]" ? [time] : []; },
    querySelector(selector) {
      if (selector === "title[data-recovered-title]" && recovered) return {};
      return selector === "h1" ? heading : null;
    },
    createElement(tag) { return { tagName: tag, dataset: {} }; },
  };
  const context = vm.createContext({ document, setInterval() {}, Intl: {
    DateTimeFormat: function (locale, options) {
      assert.equal(locale, undefined, "use the browser locale");
      assert.equal(options.timeZone, undefined, "use the browser timezone");
      return new Intl.DateTimeFormat("en-US", { ...options, timeZone });
    },
  } });
  vm.runInContext(source, context);
  return { context, document, time };
}

test("initial timestamps use browser timezone across calendar boundaries", () => {
  const west = browser("America/Los_Angeles");
  assert.match(west.time.textContent, /Dec 31, 2025/);
  assert.match(west.time.textContent, /5:02:03 PM PST/);
  const east = browser("Asia/Tokyo");
  assert.match(east.time.textContent, /Jan 1, 2026/);
  assert.match(east.time.textContent, /10:02:03 AM/);
});

test("daylight saving time follows the timestamp, not today's offset", () => {
  const { context } = browser("America/Los_Angeles");
  assert.match(vm.runInContext('formatTimestamp("2026-07-01T12:00:00Z")', context), /5:00:00 AM PDT/);
  assert.match(vm.runInContext('formatTimestamp("2026-01-01T12:00:00Z")', context), /4:00:00 AM PST/);
});

test("poll updates use the same semantic timestamp and format as initial rendering", () => {
  const { context, time } = browser("America/Los_Angeles");
  context.target = { replaceChildren(child) { this.child = child; } };
  vm.runInContext('renderTimestamp(target, "2026-01-01T01:02:03Z")', context);
  assert.equal(context.target.child.tagName, "time");
  assert.equal(context.target.child.dateTime, time.dateTime);
  assert.equal(context.target.child.textContent, time.textContent);
});

test("recovered names and page titles are localized; ordinary names are untouched", () => {
  const { context, document, time } = browser("America/Los_Angeles", true);
  assert.equal(document.title, `Recovered print — ${time.textContent} · Snapshot Buddy`);
  const name = vm.runInContext('localizedSessionName({ recovered: true, opened_at: "2026-01-01T01:02:03Z", name: "UTC fallback" })', context);
  assert.equal(name, `Recovered print — ${time.textContent}`);
  assert.equal(vm.runInContext('localizedSessionName({ recovered: false, name: "Keep UTC in this filename" })', context), "Keep UTC in this filename");
  assert.equal(vm.runInContext('localizedSessionName({ recovered: true, display_name: "My vase", opened_at: "2026-01-01T01:02:03Z", name: "UTC fallback" })', context), "My vase");
});

test("missing, zero, and invalid timestamps do not become misleading dates", () => {
  const { context } = browser("Europe/Berlin");
  for (const value of [null, "", "0001-01-01T00:00:00Z", "invalid"]) {
    context.value = value;
    assert.equal(vm.runInContext("formatTimestamp(value)", context), null);
  }
  context.target = {};
  vm.runInContext('renderTimestamp(target, "0001-01-01T00:00:00Z")', context);
  assert.equal(context.target.textContent, "No markers yet");
});

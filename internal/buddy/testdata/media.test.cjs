const assert = require("node:assert/strict");
const { readFileSync } = require("node:fs");
const { join } = require("node:path");
const { test } = require("node:test");
const vm = require("node:vm");
const app = readFileSync(join(__dirname, "../web/app.js"), "utf8");
const live = readFileSync(join(__dirname, "../web/live.js"), "utf8");

function downloads(state = "queued", intent = "job1", requestedHere = true) {
  let clicks = 0, replace = 0;
  const link = { click() { clicks++; } };
  const job = { dataset: { job: "job1", state }, querySelector() { return link; } };
  const jobs = { buddyDownloadRequested: requestedHere, dataset: { autoDownload: intent }, querySelectorAll() { return [job]; } };
  const toast = { classList: { add() {}, remove() {} } };
  const window = { location: { href: "http://localhost/sessions/one?download=job1#videos" }, history: { state: {}, replaceState(_state, _title, url) { replace++; window.location.href = url.toString(); } } };
  const document = { querySelectorAll() { return []; }, querySelector(selector) { return selector === "[data-auto-download]" ? jobs : selector === "#toast" ? toast : null; } };
  const context = vm.createContext({ document, window, URL, setTimeout() {}, setInterval() {}, Intl });
  vm.runInContext(app, context);
  return { context, job, jobs, window, clicks: () => clicks, replacements: () => replace };
}

test("a newly completed export downloads exactly once, consuming only its query parameter", () => {
  const b = downloads();
  assert.equal(b.clicks(), 0);
  b.job.dataset.state = "ready";
  vm.runInContext("downloadRequestedVideo(); downloadRequestedVideo();", b.context);
  assert.equal(b.clicks(), 1);
  assert.equal(b.replacements(), 1);
  assert.equal(b.window.location.href, "http://localhost/sessions/one#videos");
});
test("cached exports download immediately; failed and unrelated jobs never download", () => {
  assert.equal(downloads("ready").clicks(), 1);
  assert.equal(downloads("failed").clicks(), 0);
  assert.equal(downloads("ready", "").clicks(), 0);
  assert.equal(downloads("ready", "other-job").clicks(), 0);
  assert.equal(downloads("running").clicks(), 0);
  assert.equal(downloads("ready", "job1", false).clicks(), 0);
});

function liveContext(extra = {}) {
  const context = vm.createContext({ document: { querySelector() { return null; }, hidden: false }, window: {}, setTimeout, clearTimeout, ...extra });
  vm.runInContext(live, context);
  return context;
}
test("expanded video retains native size and aspect ratio across screen sizes", () => {
  const c = liveContext();
  for (const [width, height, aw, ah, wantWidth, wantHeight] of [
    [640, 480, 1400, 1000, 640, 480], [1920, 1080, 900, 700, 900, 506],
    [640, 480, 327, 660, 327, 245], [640, 480, 700, 200, 266, 200],
    [0, 0, 100, 100, 0, 0],
  ]) {
    const result = vm.runInContext(`fittedVideoSize(${width},${height},${aw},${ah})`, c);
    assert.equal(result.width, wantWidth); assert.equal(result.height, wantHeight);
  }
});
test("ICE gathering completes or cancels without leaking listeners", async () => {
  const c = liveContext();
  const peer = new EventTarget(); peer.iceGatheringState = "gathering";
  const controller = new AbortController();
  c.peer = peer; c.signal = controller.signal;
  const complete = vm.runInContext("waitForICE(peer, signal)", c);
  peer.iceGatheringState = "complete"; peer.dispatchEvent(new Event("icegatheringstatechange"));
  await complete;
  peer.iceGatheringState = "gathering";
  const cancelled = vm.runInContext("waitForICE(peer, signal)", c);
  controller.abort();
  await assert.rejects(cancelled, /Cancelled/);
});
test("live preview cleanup closes peers, cancels signaling, clears media, and cancels retry timers", () => {
  let closed = 0, aborted = 0, stopped = 0, sheetUpdates = 0;
  const video = { srcObject: { getTracks: () => [{ stop() { stopped++; } }] }, addEventListener() {} };
  const button = { querySelector(selector) { return selector === "video" ? video : {}; }, addEventListener() {} };
  const c = liveContext({ button, sheet: { update() { sheetUpdates++; } } });
  const preview = vm.runInContext("new LivePreview(button, sheet)", c);
  preview.peer = { close() { closed++; } }; preview.controller = { abort() { aborted++; } };
  preview.stop();
  assert.equal(closed, 1); assert.equal(aborted, 1); assert.equal(stopped, 1);
  assert.equal(sheetUpdates, 1); assert.equal(video.srcObject, null); assert.equal(preview.peer, null);
});
test("reconnection backs off and hidden tabs do not schedule retries", () => {
  const scheduled = [];
  const video = { addEventListener() {} };
  const button = { querySelector(selector) { return selector === "video" ? video : {}; }, addEventListener() {} };
  const c = liveContext({ button, sheet: { update() {} }, setTimeout(_fn, delay) { scheduled.push(delay); }, clearTimeout() {} });
  const preview = vm.runInContext("new LivePreview(button, sheet)", c);
  for (let i=0; i<6; i++) preview.retry();
  assert.deepEqual(scheduled, [3000, 6000, 12000, 24000, 30000, 30000]);
  c.document.hidden = true; preview.retry();
  assert.equal(scheduled.length, 6);
});

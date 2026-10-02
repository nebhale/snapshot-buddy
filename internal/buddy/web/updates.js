"use strict";

// Keep this small transport and DOM reconciler identical in both Buddy apps.
// The Go templates own presentation; the browser owns drafts and interaction.
function buddyKey(node) {
  if (node.nodeType !== 1) return "";
  if (node.dataset.key || node.id) return node.dataset.key || node.id;
  if (node.tagName === "FORM") return `form:${node.getAttribute("action")}:${node.querySelector('[name="section"]')?.value || ""}:${node.querySelector('[name="operation"]')?.value || ""}`;
  if (node.getAttribute("name")) return `${node.tagName}:${node.getAttribute("name")}`;
  return "";
}
function buddyValue(field) {
  if (!field) return "";
  if (field.name === "display_name") return field.value.trim();
  return field.value;
}
function buddyEditable(form) { return form.querySelector('[name="spool"], [name="duration"], [name="display_name"]'); }
function buddyFormState(form) {
  if (!form.buddyState) {
    const field = buddyEditable(form);
    form.buddyState = { base: buddyValue(field), latest: buddyValue(field), expected: form.querySelector('[name^="expected_"]')?.value, conflict: false, saving: false };
  }
  return form.buddyState;
}
function buddyMessage(form, text) {
  let message = form.querySelector(':scope > .form-message');
  if (!message) {
    message = document.createElement("div");
    message.className = "form-message";
    message.dataset.clientOwned = "";
    message.setAttribute("role", "status");
    form.append(message);
  }
  delete message.buddyConflictKey;
  message.replaceChildren(document.createTextNode(text));
  return message;
}
function buddyConflict(form) {
  const state = buddyFormState(form);
  const field = buddyEditable(form);
  const key = JSON.stringify([state.latest, buddyValue(field)]);
  if (form.querySelector(':scope > .form-message')?.buddyConflictKey === key) return;
  const label = (value) => field.name === "display_name" ? (value || "Original name") : field.name === "spool" ? (window.BuddyPicker?.label(value) || `Spool #${value}`) : value;
  const message = buddyMessage(form, `Saved value: ${label(state.latest)}. Your edit: ${label(buddyValue(field))}. `);
  message.buddyConflictKey = key;
  for (const [text, mine] of [["Use saved value", false], ["Save my value", true]]) {
    const button = document.createElement("button");
    button.type = "button"; button.className = "button secondary"; button.textContent = text;
    button.addEventListener("click", () => {
      state.base = state.latest; state.conflict = false;
      const expected = form.querySelector('[name^="expected_"]');
      if (expected) expected.value = state.expected;
      if (!mine) { field.value = state.latest; window.BuddyPicker?.sync(form); message.remove(); }
      else form.requestSubmit(form.querySelector('button:not([type="button"])'));
    });
    message.append(button);
  }
}
function buddyRetainDraft(node) {
  if (node.nodeType !== 1) return false;
  const forms = node.tagName === "FORM" ? [node] : Array.from(node.querySelectorAll("form"));
  if (!forms.some(form => form.buddyState && (form.buddyState.saving || form.buddyState.unconfirmed || buddyValue(buddyEditable(form)) !== form.buddyState.base))) return false;
  if (!node.dataset.removedDraft) {
    node.dataset.removedDraft = "true";
    for (const form of forms) for (const button of form.querySelectorAll("button")) button.disabled = true;
    const notice = document.createElement("p"); notice.className = "form-message"; notice.dataset.clientOwned = "";
    notice.textContent = "This section or action was removed elsewhere. Your draft is kept here. ";
    const discard = document.createElement("button"); discard.type = "button"; discard.className = "button secondary"; discard.textContent = "Discard draft";
    discard.addEventListener("click", () => node.remove()); notice.append(discard); node.append(notice);
  }
  return true;
}
function buddyPatch(old, fresh, preserveValue = false) {
  if (old.nodeType !== fresh.nodeType || old.nodeName !== fresh.nodeName) { old.replaceWith(fresh.cloneNode(true)); return; }
  if (old.nodeType !== 1) { if (old.textContent !== fresh.textContent) old.textContent = fresh.textContent; return; }
  if (old.hasAttribute("data-preserve") || old.hasAttribute("data-client-owned")) return;
  if (old.classList.contains("native-spool-label") && old.closest("form")?.dataset.pickerReady) return;
  let state;
  if (old.tagName === "FORM") {
    state = buddyFormState(old);
    if (state.saving) return;
    const field = buddyEditable(old), next = buddyEditable(fresh);
    let dirty = buddyValue(field) !== state.base;
    state.latest = buddyValue(next);
    dirty ||= field === document.activeElement && buddyValue(field) !== state.latest;
    state.dirty = dirty;
    state.expected = fresh.querySelector('[name^="expected_"]')?.value;
    const localOnly = field?.name === "duration";
    preserveValue = localOnly || dirty || old.contains(document.activeElement);
    state.conflict = dirty && state.latest !== state.base && Boolean(old.querySelector('[name^="expected_"]'));
    if (!dirty && !localOnly) {
      state.base = state.latest;
      if (old.buddyPicker) { old.buddyPicker.ensureOption(state.latest); field.value = state.latest; }
    }
  }
  const clientAttributes = new Set(["data-auto-download", "data-picker-ready"]);
  for (const attr of Array.from(old.attributes)) {
    if (clientAttributes.has(attr.name) || (old.tagName === "DETAILS" && attr.name === "open")) continue;
    if (preserveValue && ["value", "selected"].includes(attr.name)) continue;
    if (!fresh.hasAttribute(attr.name)) old.removeAttribute(attr.name);
  }
  for (const attr of fresh.attributes) {
    if (clientAttributes.has(attr.name) || (old.tagName === "DETAILS" && attr.name === "open")) continue;
    if (preserveValue && ["value", "selected"].includes(attr.name)) continue;
    if (old.getAttribute(attr.name) !== attr.value) old.setAttribute(attr.name, attr.value);
  }
  if (["INPUT", "SELECT", "TEXTAREA"].includes(old.tagName)) {
    if (old.type === "hidden" && (!old.name.startsWith("expected_") || !old.form?.buddyState?.dirty)) old.value = fresh.value;
    else if (!preserveValue) old.value = fresh.value;
    // Enhanced selects belong to their picker. Its shared catalog owns options.
    if (old.tagName !== "SELECT" || old.closest("form")?.dataset.pickerReady) return;
  }
  const previous = Array.from(old.childNodes).filter(n => !(n.nodeType === 1 && n.hasAttribute("data-client-owned")));
  const used = new Set();
  let position = old.firstChild;
  for (const child of fresh.childNodes) {
    while (position?.nodeType === 1 && position.hasAttribute("data-client-owned")) position = position.nextSibling;
    const key = buddyKey(child);
    const match = previous.find(n => !used.has(n) && (key ? buddyKey(n) === key : !buddyKey(n) && n.nodeName === child.nodeName));
    if (match) {
      used.add(match);
      if (match !== position) old.insertBefore(match, position);
      buddyPatch(match, child, preserveValue);
      position = match.parentNode === old ? match.nextSibling : old.firstChild;
    } else {
      const clone = child.cloneNode(true); old.insertBefore(clone, position); position = clone.nextSibling;
    }
  }
  for (const child of previous) if (!used.has(child) && child.parentNode === old && !buddyRetainDraft(child)) child.remove();
  if (old.tagName === "SELECT" && !preserveValue) old.value = fresh.value;
  if (state) {
    if (state.conflict) buddyConflict(old);
    else if (!state.error && buddyValue(buddyEditable(old)) === state.latest) old.querySelector(':scope > .form-message')?.remove();
    window.BuddyPicker?.sync(old);
  }
}

class BuddyUpdates {
  constructor(root) {
    this.root = root; this.known = {}; this.instance = root.dataset.instance;
    this.generation = 0; this.delay = 1000; this.pending = false; this.deferred = new Map();
    this.status = document.getElementById("connection-state");
    this.status.hidden = false;
    document.querySelectorAll('form[method="post"]').forEach(buddyFormState);
    document.addEventListener("submit", event => this.submit(event));
    document.addEventListener("input", event => {
      const form = event.target.form;
      if (form?.buddyState?.conflict) buddyConflict(form);
    });
    document.addEventListener("pointerdown", event => { this.pointerRow = event.target.closest(".session-entry,.session-row"); });
    const release = () => { this.pointerRow = null; setTimeout(() => this.flushDeferred(), 0); };
    document.addEventListener("pointerup", release); document.addEventListener("pointercancel", release);
    document.addEventListener("focusout", () => setTimeout(() => this.flushDeferred(), 0));
    document.addEventListener("toggle", event => { if (event.target.id === "provenance" && event.target.open) this.refresh(); }, true);
    document.addEventListener("visibilitychange", () => document.hidden ? this.stop() : this.start());
    window.addEventListener("pagehide", () => this.stop());
    window.addEventListener("pageshow", event => { if (event.persisted) this.start(); });
    window.addEventListener("focus", () => { if (this.auth && !document.hidden) { this.auth = false; this.start(); } });
    const bulkForm = document.getElementById("bulk-form");
    if (bulkForm && window.BuddyBulk) this.bulk = new window.BuddyBulk(bulkForm, this);
    this.filter = document.querySelector(".filter select");
    if (this.filter) window.addEventListener("popstate", () => this.filterLibrary(new URL(window.location.href), false));
    this.start();
  }
  filterLibrary(url, push = true) {
    if (this.bulk.saving || this.bulk.unconfirmed || this.auth) {
      const current = new URL(this.bulk.form.dataset.libraryUrl, window.location.href);
      this.filter.value = current.searchParams.get("printer") || "";
      if (!push) window.history.replaceState(window.history.state, "", current);
      return;
    }
    if (push && url.href !== window.location.href) window.history.pushState(null, "", url);
    this.filter.value = url.searchParams.get("printer") || "";
    this.bulk.form.dataset.libraryUrl = url.pathname + url.search;
    this.bulk.form.querySelector('input[name="printer"]').value = this.filter.value;
    this.bulk.form.querySelector('input[name="page"]').value = url.searchParams.get("page") || "0";
    this.bulk.selected.clear(); this.bulk.selecting = false;
    this.bulk.results.hidden = true;
    this.bulk.form.querySelector('[data-selection-notice]').hidden = true;
    // Discard responses and deferred rows from the previous filter.
    this.generation++; this.controller?.abort(); this.deferred.clear();
    delete this.known["library-list"]; delete this.known.pagination;
    this.filtering = true;
    document.getElementById("library-list").setAttribute("aria-busy", "true");
    this.bulk.sync();
    this.refresh();
  }
  connection(text) { this.status.textContent = text; }
  stop() {
    this.generation++; this.streaming = false;
    this.source?.close(); this.source = null;
    clearTimeout(this.retryTimer); clearTimeout(this.pollTimer); clearTimeout(this.watchdog);
    this.controller?.abort();
  }
  start() {
    this.stop(); if (document.hidden || this.auth) return;
    this.connection("Connecting…");
    this.connect(); this.refresh();
  }
  connect() {
    if (document.hidden || this.auth) return;
    if (!window.EventSource) { this.fallback(); return; }
    const source = this.source = new EventSource("/api/events");
    const alive = (event) => {
      if (source !== this.source) return;
      if (event.data !== this.instance) { this.instance = event.data; this.known = {}; this.generation++; this.controller?.abort(); }
      clearTimeout(this.watchdog);
      this.watchdog = setTimeout(() => this.lost(source), 45000);
      this.delay = 1000; this.streaming = true;
      clearTimeout(this.pollTimer); this.connection("Live");
    };
    source.addEventListener("change", event => { alive(event); this.refresh(); });
    source.addEventListener("heartbeat", alive);
    source.onerror = () => this.lost(source);
    this.watchdog = setTimeout(() => this.lost(source), 45000);
  }
  lost(source) {
    if (source !== this.source) return;
    source.close(); this.source = null; this.streaming = false;
    clearTimeout(this.watchdog); this.fallback();
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => this.connect(), this.delay);
    this.delay = Math.min(this.delay * 2, 30000);
  }
  fallback() {
    if (document.hidden || this.auth) return;
    this.streaming = false; clearTimeout(this.pollTimer);
    this.connection("Updating every 3 seconds"); this.refresh();
    this.pollTimer = setTimeout(() => this.fallback(), 3000);
  }
  async refresh() {
    if (document.hidden || this.auth) return;
    if (this.fetching) { this.pending = true; return; }
    this.fetching = true;
    const generation = this.generation;
    const controller = this.controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15000);
    try {
      const params = new URLSearchParams(window.location.search);
      params.delete("download"); params.set("view", this.root.dataset.view);
      params.set("id", this.root.dataset.session || "");
      params.set("known", JSON.stringify(this.known));
      params.set("audit", String(Boolean(document.querySelector("#provenance[open]"))));
      const frames = Array.from(document.querySelectorAll("[data-capture]"));
      params.set("after", String(frames.reduce((last, n) => Math.max(last, Number(n.dataset.capture)), 0)));
      params.set("pending", frames.filter(n => n.dataset.captureState === "pending").map(n => n.dataset.capture).join(","));
      const response = await fetch(`/api/live?${params}`, { signal: controller.signal, headers: { Accept: "application/json" } });
      if (generation !== this.generation) return;
      if (response.status === 401 || response.status === 403) {
        this.auth = true; this.stop(); this.connection("Sign-in required · sign in in another tab, then return here"); return;
      }
      if (response.status === 404) { this.stop(); this.connection("This session was removed · return to the print library"); this.removed = true; return; }
      if (!response.ok) throw new Error("Unable to retrieve current state");
      const data = await response.json();
      if (generation !== this.generation) return;
      if (data.instance !== this.instance) { this.instance = data.instance; this.known = {}; }
      for (const input of document.querySelectorAll('input[name="csrf"]')) input.value = data.csrf;
      for (const preview of document.querySelectorAll("[data-live]")) preview.dataset.csrf = data.csrf;
      for (const region of data.regions) this.apply(region);
      if (data.revision !== undefined) for (const input of document.querySelectorAll('input[name="revision"]')) input.value = data.revision;
      if (data.catalog) { this.known.catalog = data.catalog.version; window.BuddyPicker?.catalog(data.catalog.data); }
      for (const form of document.querySelectorAll('form[method="post"]')) {
        const state = buddyFormState(form);
        if (state.unconfirmed) {
          state.unconfirmed = false;
          state.restoreButtons?.();
          if (!state.conflict) buddyMessage(form, "Current state refreshed. Review the saved value before submitting again.");
        }
      }
      if (this.filtering) {
        this.filtering = false;
        document.getElementById("library-list").removeAttribute("aria-busy");
      }
      this.afterUpdate();
      this.connection(this.streaming ? "Live" : "Updating every 3 seconds");
      this.root.dispatchEvent(new CustomEvent("buddy:updated"));
    } catch (error) {
      if (generation === this.generation) {
        if (this.source) this.lost(this.source);
        this.connection("Disconnected · keeping your view and retrying…");
      }
    } finally {
      clearTimeout(timeout); this.fetching = false;
      if (this.pending) { this.pending = false; this.refresh(); }
    }
  }
  apply(region) {
    const target = document.getElementById(region.id);
    if (!target) return;
    if (this.bulk?.saving && ["library-list", "pagination"].includes(region.id)) { this.deferred.set(region.id, region); return; }
    if (region.id === "library-list" && !this.filtering && (this.pointerRow || target.contains(document.activeElement))) { this.deferred.set(region.id, region); return; }
    this.deferred.delete(region.id);
    const template = document.createElement("template"); template.innerHTML = region.html;
    const fresh = template.content.firstElementChild;
    if (!fresh || fresh.id !== region.id) return;
    // Measure the layout with localized dates, including narrow library rows.
    for (const time of fresh.querySelectorAll("time[datetime]")) { const text = formatTimestamp(time.dateTime); if (text) time.textContent = text; }
    const anchors = Array.from(document.querySelectorAll(".session-row,.section-card,[data-capture]")).map(node => ({ node, rect: node.getBoundingClientRect() })).filter(({ rect }) => rect.bottom > 0 && rect.top < window.innerHeight);
    const scroll = window.scrollY;
    if (region.append) {
      for (const node of fresh.querySelectorAll("[data-capture]")) {
        const old = Array.from(target.children).find(n => n.dataset.capture === node.dataset.capture);
        if (old) buddyPatch(old, node); else target.append(node.cloneNode(true));
      }
      if (target.querySelector("[data-capture]")) target.querySelector(".empty")?.remove();
    } else buddyPatch(target, fresh);
    const anchor = anchors.find(({ node }) => node.isConnected);
    if (scroll > 0 && anchor) window.scrollBy(0, anchor.node.getBoundingClientRect().top - anchor.rect.top);
    this.known[region.id] = region.version;
  }
  flushDeferred() {
    for (const [id, region] of Array.from(this.deferred)) { this.deferred.delete(id); this.apply(region); }
    this.afterUpdate();
  }
  afterUpdate() {
    this.bulk?.sync();
    this.bulk?.recover();
    document.querySelectorAll('form[method="post"]').forEach(buddyFormState);
    for (const time of document.querySelectorAll("time[datetime]")) { const text = formatTimestamp(time.dateTime); if (text) time.textContent = text; }
    window.BuddyPicker?.enhance();
    window.BuddyName?.sync();
    if (typeof updateVideoRate === "function") updateVideoRate();
    if (typeof downloadRequestedVideo === "function") downloadRequestedVideo();
    const title = document.querySelector("title[data-app-name]");
    if (title && this.root.dataset.view === "session") document.title = `${document.querySelector("h1").textContent} · ${title.dataset.appName}`;
  }
  async submit(event) {
    const form = event.target;
    if (form === this.filter?.form) {
      event.preventDefault();
      const url = new URL(form.action);
      url.search = new URLSearchParams(new FormData(form)).toString();
      this.filterLibrary(url);
      return;
    }
    if (form.method.toLowerCase() !== "post") return;
    event.preventDefault();
    if (form === this.bulk?.form) { await this.bulk.submit(); return; }
    const state = buddyFormState(form);
    if (state.saving || state.unconfirmed || this.auth || this.removed || form.closest("[data-removed-draft]")) return;
    if (state.conflict) { buddyConflict(form); return; }
    if (form.dataset.confirm && !window.confirm(form.dataset.confirm)) return;
    const field = buddyEditable(form), submitted = buddyValue(field);
    const body = new URLSearchParams(new FormData(form));
    if (event.submitter?.name) body.set(event.submitter.name, event.submitter.value);
    state.saving = true; state.error = false;
    const buttons = Array.from(form.querySelectorAll("button"));
    const disabled = buttons.map(b => b.disabled); buttons.forEach(b => { b.disabled = true; });
    state.restoreButtons = () => buttons.forEach((button, i) => { button.disabled = disabled[i]; });
    buddyMessage(form, "Saving…");
    try {
      const response = await fetch(form.action, { method: "POST", body, headers: { Accept: "application/json" }, signal: AbortSignal.timeout(20000) });
      let data;
      try { data = await response.json(); } catch {
        if (response.ok) throw new TypeError("Unrecognized save response");
        data = {};
      }
      if (!response.ok) {
        if (response.status === 401) { this.auth = true; this.stop(); this.connection("Sign-in required · sign in in another tab, then return here"); }
        throw new Error(data.error || (response.status === 403 ? "Page credentials changed. Review your edit and save again." : "Unable to save. Review the current state and try again."));
      }
      state.base = submitted; state.conflict = false;
      window.BuddyName?.saved(form, submitted);
      window.BuddyPicker?.saved(form, submitted);
      buddyMessage(form, "Saved");
      if (data.location) {
        const url = new URL(data.location, window.location.href);
        if (form.action.endsWith("/delete")) { window.location.assign(url); return; }
        const job = url.searchParams.get("download");
        if (job) {
          const jobs = document.querySelector("[data-auto-download]");
          if (jobs) { jobs.buddyDownloadRequested = true; jobs.dataset.autoDownload = job; }
          window.history.replaceState(window.history.state, "", url);
        }
      }
    } catch (error) {
      state.error = true;
      state.unconfirmed = error.name === "TypeError" || error.name === "TimeoutError";
      buddyMessage(form, state.unconfirmed ? "Could not confirm whether this was saved. Checking current state; your edit is kept. Review before saving again." : error.message);
    } finally {
      state.saving = false;
      if (!state.unconfirmed) state.restoreButtons();
      window.BuddyPicker?.sync(form);
      this.known = {}; await this.refresh();
    }
  }
}
const buddyRoot = document.querySelector('main[data-view="dashboard"], main[data-view="session"]');
if (buddyRoot) window.buddyUpdates = new BuddyUpdates(buddyRoot);

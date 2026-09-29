"use strict";

// Keep library selection behavior identical in both Buddy apps.
class BuddyBulk {
  constructor(form, updates) {
    this.form = form; this.updates = updates;
    this.action = form.dataset.bulkAction;
    this.selected = new Set(this.boxes().filter(box => box.checked).map(box => box.value));
    this.all = form.querySelector('[data-select-all]');
    this.clear = form.querySelector('[data-clear-selection]');
    this.button = form.querySelector('[data-bulk-submit]');
    this.count = form.querySelector('[data-selection-count]');
    this.results = form.querySelector('[data-bulk-results]');
    form.querySelector('[data-bulk-enhanced]').hidden = false;
    // Native POST results use this URL for subsequent live reads and pagination.
    window.history.replaceState(window.history.state, "", form.dataset.libraryUrl);
    form.addEventListener('change', event => {
      if (event.target === this.all) {
        for (const box of this.boxes()) if (box.dataset.eligible === 'true') {
          if (this.all.checked) this.selected.add(box.value); else this.selected.delete(box.value);
        }
      } else if (event.target.matches('[data-session-select]')) {
        if (event.target.checked) this.selected.add(event.target.value); else this.selected.delete(event.target.value);
      } else return;
      this.sync();
    });
    this.clear.addEventListener('click', () => { this.selected.clear(); this.sync(); });
    window.addEventListener('pageshow', event => {
      if (event.persisted && !this.saving && !this.unconfirmed) { this.selected.clear(); this.sync(); }
    });
    this.sync();
  }
  boxes() { return Array.from(this.form.querySelectorAll('[data-session-select]')); }
  sync() {
    const boxes = this.boxes();
    const eligible = boxes.filter(box => box.dataset.eligible === 'true');
    const available = new Set(eligible.map(box => box.value));
    let removed = 0;
    if (!this.saving && !this.unconfirmed) for (const id of this.selected) {
      if (!available.has(id)) { this.selected.delete(id); removed++; }
    }
    if (removed) {
      const notice = this.form.querySelector('[data-selection-notice]');
      notice.textContent = `${removed} selected ${removed === 1 ? 'session is' : 'sessions are'} no longer selectable on this page and were deselected.`;
      notice.hidden = false;
    }
    const locked = Boolean(this.saving || this.unconfirmed || this.updates.auth);
    for (const box of boxes) {
      box.checked = this.selected.has(box.value);
      box.disabled = locked || box.dataset.eligible !== 'true';
    }
    this.all.disabled = locked || eligible.length === 0;
    this.all.checked = eligible.length > 0 && eligible.every(box => this.selected.has(box.value));
    this.all.indeterminate = this.selected.size > 0 && !this.all.checked;
    this.count.textContent = `${this.selected.size} selected`;
    this.form.querySelector('.bulk-actions').hidden = this.selected.size === 0;
    this.clear.hidden = this.selected.size === 0;
    this.clear.disabled = locked;
    this.button.disabled = locked || this.selected.size === 0;
  }
  show(message, failures = []) {
    const p = document.createElement('p'); p.textContent = message;
    this.results.replaceChildren(p);
    if (failures.length) {
      const list = document.createElement('ul');
      for (const item of failures) {
        const li = document.createElement('li'); li.textContent = `${item.name}: ${item.error}`; list.append(li);
      }
      this.results.append(list);
    }
    this.results.hidden = false;
  }
  async submit() {
    if (this.saving || this.unconfirmed || this.updates.auth) return;
    this.sync();
    if (!this.selected.size) return;
    const items = this.boxes().filter(box => this.selected.has(box.value)).map(box => ({
      id: box.value, name: box.closest('.session-entry').querySelector('h3').textContent,
    }));
    if (this.action === 'delete' && !window.confirm(`Permanently delete ${items.length} selected ${items.length === 1 ? 'session' : 'sessions'}, including all their snapshots and videos? This cannot be undone.`)) return;
    const body = new URLSearchParams(new FormData(this.form));
    if (this.action === 'delete') body.set('confirmed', 'true');
    this.saving = true; this.sync(); this.show('Processing selected sessions…');
    try {
      const response = await fetch(this.form.action, { method: 'POST', body, headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(20000) });
      if (response.status >= 500) throw new TypeError('The result could not be confirmed');
      if (response.status === 401) {
        this.updates.auth = true; this.updates.stop();
        this.updates.connection('Sign-in required · sign in in another tab, then return here');
      }
      let data;
      try { data = await response.json(); } catch {
        if (response.ok) throw new TypeError('Unrecognized action response');
        data = {};
      }
      if (!response.ok) throw new Error(data.error || (response.status === 403 ? 'Page credentials changed. Review your selection and try again.' : 'Unable to process the selection. Review it and try again.'));
      const expected = new Set(items.map(item => item.id));
      if (!Array.isArray(data.results) || data.results.length !== items.length || data.results.some(item => !expected.delete(item.id) || !['success', 'conflict', 'missing', 'error'].includes(item.status))) throw new TypeError('Incomplete action response');
      for (const item of data.results) if (item.status === 'success') this.selected.delete(item.id);
      this.show(data.summary, data.results.filter(item => item.status !== 'success'));
      if (data.location) {
        const url = new URL(data.location, window.location.href);
        if (url.origin === window.location.origin && url.pathname === '/') {
          window.history.replaceState(window.history.state, '', url);
          this.form.querySelector('[name="page"]').value = url.searchParams.get('page') || '0';
        }
      }
    } catch (error) {
      if (['TypeError', 'TimeoutError', 'AbortError'].includes(error.name)) {
        this.unconfirmed = items;
        this.show('Could not confirm the results. Checking each selected session; no action will be repeated automatically.');
      } else this.show(error.message);
    } finally {
      this.saving = false;
      // A deferred list may predate this action. Obtain a fresh authoritative one.
      this.updates.deferred.delete('library-list'); this.updates.deferred.delete('pagination');
      this.updates.known = {};
      this.sync();
      await this.updates.refresh();
      await this.recover();
    }
  }
  async recover() {
    if (!this.unconfirmed || this.recovering || this.updates.auth || document.hidden) return;
    this.recovering = true;
    try {
      const states = await Promise.all(this.unconfirmed.map(async item => {
        const response = await fetch(`/api/sessions/${encodeURIComponent(item.id)}`, { headers: { Accept: 'application/json' }, signal: AbortSignal.timeout(15000) });
        if (response.status === 404) return { id: item.id, done: true };
        if (!response.ok) throw new Error('Unable to check session state');
        const session = await response.json();
        const archived = session.Archived ?? session.archived;
        return { id: item.id, done: this.action === 'archive' ? archived === true : this.action === 'restore' ? archived === false : false };
      }));
      for (const state of states) if (state.done) this.selected.delete(state.id);
      this.unconfirmed = null;
      this.show('Current session states checked. Review the remaining selection before trying again.');
      this.sync();
    } catch {
      this.show('Results are still unconfirmed. Waiting to check every selected session; no action will be repeated automatically.');
    } finally { this.recovering = false; }
  }
}
window.BuddyBulk = BuddyBulk;

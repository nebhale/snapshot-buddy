"use strict";

// Keep the inline name editor identical in both Buddy apps.
window.BuddyName = {
  close(form) {
    const editor = form.closest(".name-edit");
    const focused = editor.contains(document.activeElement);
    editor.open = false;
    if (focused) editor.querySelector("summary").focus({ preventScroll: true });
  },
  cancel(form) {
    const state = buddyFormState(form);
    if (state.saving || state.unconfirmed) return;
    form.querySelector('[name="display_name"]').value = state.latest;
    form.querySelector('[name="expected_display_name"]').value = state.expected;
    state.base = state.latest;
    state.dirty = state.conflict = state.error = false;
    delete form.buddyNameSaved;
    form.querySelector(":scope > .form-message")?.remove();
    this.close(form);
  },
  saved(form, value) {
    if (form.classList.contains("name-form")) form.buddyNameSaved = value;
  },
  sync() {
    for (const form of document.querySelectorAll(".name-form")) {
      const field = form.querySelector('[name="display_name"]');
      const opened = field.dataset.recoveredAt && formatTimestamp(field.dataset.recoveredAt);
      if (opened) field.placeholder = `Recovered print — ${opened}`;
      const saved = form.buddyNameSaved;
      if (saved === undefined) continue;
      const state = buddyFormState(form);
      if (state.saving || state.unconfirmed || state.error) continue;
      if (buddyValue(field) !== saved || state.conflict) {
        delete form.buddyNameSaved;
      } else if (state.latest === saved && state.expected === saved) {
        // Close only once the acknowledged name is present in the live heading.
        field.value = saved;
        delete form.buddyNameSaved;
        this.close(form);
      }
    }
  },
};

document.addEventListener("click", event => {
  const pencil = event.target.closest(".name-edit > summary");
  if (pencil) {
    event.preventDefault();
    const editor = pencil.parentElement;
    const field = editor.querySelector('[name="display_name"]');
    editor.open = true;
    field.focus({ preventScroll: true });
    field.select();
    return;
  }
  const cancel = event.target.closest("[data-cancel-name]");
  if (!cancel) return;
  event.preventDefault();
  window.BuddyName.cancel(cancel.closest("form"));
});
document.addEventListener("keydown", event => {
  if (event.key !== "Escape" || event.isComposing) return;
  const form = event.target.closest(".name-form");
  if (!form) return;
  event.preventDefault();
  window.BuddyName.cancel(form);
});

// Disabling a focused Save button would otherwise move focus to the page body.
document.addEventListener("submit", event => {
  const form = event.target;
  if (form.classList.contains("name-form") && form.contains(document.activeElement)) {
    form.querySelector('[name="display_name"]').focus({ preventScroll: true });
  }
}, true);

window.BuddyName.sync();

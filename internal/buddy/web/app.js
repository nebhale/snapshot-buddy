"use strict";
const browserDateFormat = new Intl.DateTimeFormat(undefined, {
  year: "numeric", month: "short", day: "numeric",
  hour: "numeric", minute: "2-digit", second: "2-digit", timeZoneName: "short",
});
function formatTimestamp(value) {
  if (!value || value.startsWith("0001-")) return null;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? browserDateFormat.format(date) : null;
}
function localizeTime(element) {
  const text = formatTimestamp(element.dateTime);
  if (text) element.textContent = text;
}
function renderTimestamp(target, value) {
  if (!formatTimestamp(value)) {
    target.textContent = "No markers yet";
    return;
  }
  const time = document.createElement("time");
  time.dateTime = value;
  time.dataset.localTime = "";
  localizeTime(time);
  target.replaceChildren(time);
}
function localizedSessionName(session) {
  const opened = session.recovered && formatTimestamp(session.opened_at);
  return opened ? `Recovered print — ${opened}` : session.name;
}
document.querySelectorAll("time[data-local-time]").forEach(localizeTime);
if (document.querySelector("title[data-recovered-title]")) {
  document.title = `${document.querySelector("h1").textContent} · Snapshot Buddy`;
}
const toast = (message) => {
  const target = document.querySelector("#toast");
  target.textContent = message;
  target.classList.add("visible");
  setTimeout(() => target.classList.remove("visible"), 2500);
};
document.querySelectorAll("[data-confirm]").forEach((form) => {
  form.addEventListener("submit", (event) => {
    if (!window.confirm(form.dataset.confirm)) event.preventDefault();
  });
});
document.querySelectorAll("[data-copy]").forEach((button) => {
  button.addEventListener("click", async () => {
    const pre = document.getElementById(button.dataset.copy);
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(pre.textContent);
      } else {
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(pre);
        selection.removeAllRanges();
        selection.addRange(range);
        if (!document.execCommand("copy")) throw new Error("Select and copy");
        selection.removeAllRanges();
      }
      toast("G-code copied");
    } catch {
      toast("Select the G-code and copy it with your browser");
    }
  });
});
const videoForm = document.querySelector("#video-form");
if (videoForm) {
  const input = videoForm.querySelector("#duration");
  const updateRate = () => {
    const count = Number(videoForm.dataset.frames);
    const duration = Number(input.value);
    document.querySelector("#fps-hint").textContent = duration > 0
      ? `${count} frames · ${(count / duration).toFixed(2)} fps · MP4`
      : "Enter a video length to calculate the frame rate.";
  };
  input.addEventListener("input", updateRate);
  updateRate();
}
async function poll() {
  if (document.hidden) return;
  const sessionUpdate = document.querySelector("#session-update");
  if (sessionUpdate) {
    try {
      const response = await fetch(`/api/sessions/${sessionUpdate.dataset.session}`);
      if (response.ok) {
        const session = await response.json();
        sessionUpdate.hidden = String(session.revision) === sessionUpdate.dataset.revision;
      }
    } catch { /* Keep the user's view and in-progress form intact. */ }
  }
  const cards = document.querySelectorAll("[data-printer]");
  if (cards.length) {
    try {
      const response = await fetch("/api/status");
      if (!response.ok) throw new Error("Service unreachable");
      const status = await response.json();
      for (const p of status) {
        const card = document.querySelector(`[data-printer="${p.printer.id}"]`);
        if (!card) continue;
        const state = card.querySelector('[data-role="state"]');
        state.textContent = p.active ? "Active" : p.suppressed ? "Closed manually" : "Waiting";
        state.classList.toggle("active", Boolean(p.active));
        const name = card.querySelector('[data-role="name"]');
        if (p.active) {
          const link = document.createElement("a");
          link.href = `/sessions/${p.active.id}`;
          link.textContent = localizedSessionName(p.active);
          name.replaceChildren(link);
        } else name.textContent = "No active session";
        card.querySelector('[data-role="count"]').textContent = p.active ? `${p.active.frame_count} snapshots saved` : "";
        card.querySelector('[data-role="error"]').textContent = p.last_error || "";
        card.querySelector('[data-role="suppression"]').textContent = p.suppressed ? "LAYER markers are ignored until a new START marker or application restart." : "";
        renderTimestamp(card.querySelector('[data-role="received"]'), p.last_event);
      }
    } catch {
      cards.forEach((card) => { card.querySelector('[data-role="error"]').textContent = "Connection lost. Retrying…"; });
    }
  }
  for (const job of document.querySelectorAll('[data-job][data-state="queued"], [data-job][data-state="running"]')) {
    try {
      const response = await fetch(`/api/jobs/${job.dataset.job}`);
      if (!response.ok) continue;
      const data = await response.json();
      job.dataset.state = data.state;
      job.querySelector('[data-role="job-state"]').textContent = data.state;
      job.querySelector("progress").value = data.progress;
      job.querySelector('[data-role="job-error"]').textContent = data.error || "";
      job.querySelector('[data-role="download"]').hidden = data.state !== "ready";
      downloadRequestedVideo();
    } catch { /* The next poll reconnects without interrupting a running job. */ }
  }
}
function downloadRequestedVideo() {
  const jobs = document.querySelector("[data-auto-download]");
  if (!jobs?.dataset.autoDownload) return;
  const job = Array.from(jobs.querySelectorAll("[data-job]")).find((item) => item.dataset.job === jobs.dataset.autoDownload);
  if (!job || !["ready", "failed"].includes(job.dataset.state)) return;
  // Consume the intent before clicking; polling and refresh must not repeat it.
  jobs.dataset.autoDownload = "";
  const url = new URL(window.location.href);
  url.searchParams.delete("download");
  window.history.replaceState(window.history.state, "", url);
  if (job.dataset.state === "ready") {
    job.querySelector('[data-role="download"]').click();
    toast("Video ready. If the download doesn’t start, use Download MP4.");
  }
}
downloadRequestedVideo();
setInterval(poll, 3000);

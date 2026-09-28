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
function updateVideoRate() {
 const form = document.querySelector("#video-form");
 if (!form) return;
 const count = Number(form.dataset.frames), duration = Number(form.querySelector("#duration").value);
 document.querySelector("#fps-hint").textContent = duration > 0 ? `${count} frames · ${(count / duration).toFixed(2)} fps · MP4` : "Enter a video length to calculate the frame rate.";
}
document.querySelector("#duration")?.addEventListener("input", updateVideoRate);
updateVideoRate();
function downloadRequestedVideo() {
  const jobs = document.querySelector("[data-auto-download]");
  if (!jobs?.buddyDownloadRequested || !jobs.dataset.autoDownload) return;
  const job = Array.from(jobs.querySelectorAll("[data-job]")).find((item) => item.dataset.job === jobs.dataset.autoDownload);
  if (!job || !["ready", "failed"].includes(job.dataset.state)) return;
  // Consume the intent before clicking; polling and refresh must not repeat it.
  jobs.buddyDownloadRequested = false;
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

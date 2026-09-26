"use strict";

// No camera or microphone permission is requested: these peers only receive.
function waitForICE(peer, signal) {
  return new Promise((resolve, reject) => {
    const finish = () => {
      clearTimeout(timer);
      peer.removeEventListener("icegatheringstatechange", changed);
      signal.removeEventListener("abort", aborted);
      signal.aborted ? reject(new Error("Cancelled")) : resolve();
    };
    const changed = () => { if (peer.iceGatheringState === "complete") finish(); };
    const aborted = () => finish();
    const timer = setTimeout(finish, 5000);
    peer.addEventListener("icegatheringstatechange", changed);
    signal.addEventListener("abort", aborted);
    if (signal.aborted || peer.iceGatheringState === "complete") finish();
  });
}

function fittedVideoSize(width, height, availableWidth, availableHeight) {
  if (!width || !height) return { width: 0, height: 0 };
  const scale = Math.min(1, Math.max(0, availableWidth) / width, Math.max(0, availableHeight) / height);
  return { width: Math.floor(width * scale), height: Math.floor(height * scale) };
}

class LivePreview {
  constructor(button, sheet) {
    this.button = button;
    this.video = button.querySelector("video");
    this.message = button.querySelector("[data-live-message]");
    this.caption = button.querySelector("[data-live-status]");
    this.sheet = sheet;
    this.delay = 3000;
    button.addEventListener("click", () => {
      if (!this.peer && !document.hidden) this.connect();
      this.video.play().catch(() => {});
      sheet.open(this);
    });
    this.video.addEventListener("playing", () => {
      this.delay = 3000;
      this.status("Live", "");
    });
    this.video.addEventListener("timeupdate", () => {
      if (this.peer && !this.video.paused) {
        clearTimeout(this.watchdog);
        this.watchdog = setTimeout(() => this.retry(), 25000);
      }
    });
  }

  status(caption, message) {
    this.caption.textContent = caption;
    this.message.textContent = message;
    this.message.hidden = !message;
    this.sheet.update(this);
  }

  stop() {
    clearTimeout(this.retryTimer);
    clearTimeout(this.watchdog);
    this.controller?.abort();
    this.peer?.close();
    this.peer = null;
    this.video.srcObject?.getTracks().forEach((track) => track.stop());
    this.video.srcObject = null;
    this.sheet.update(this);
  }

  retry() {
    this.stop();
    this.status("Offline", "Live preview unavailable · retrying…");
    if (!document.hidden) {
      this.retryTimer = setTimeout(() => this.connect(), this.delay);
      this.delay = Math.min(this.delay * 2, 30000);
    }
  }

  async connect() {
    this.stop();
    if (document.hidden) return;
    if (!window.RTCPeerConnection) {
      this.status("Unavailable", "This browser doesn’t support WebRTC.");
      return;
    }
    this.status("Connecting", "Connecting to camera…");
    const controller = this.controller = new AbortController();
    try {
      const peer = this.peer = new RTCPeerConnection({ iceServers: [] });
      const current = () => this.peer === peer && !controller.signal.aborted;
      this.watchdog = setTimeout(() => this.retry(), 25000);
      peer.addTransceiver("video", { direction: "recvonly" });
      peer.ontrack = ({ track }) => {
        if (!current()) return;
        this.video.srcObject = new MediaStream([track]);
        this.sheet.update(this);
        this.video.play().catch(() => {
          if (current()) this.status("Paused", "Tap to play live video");
        });
      };
      peer.onconnectionstatechange = () => {
        if (current() && ["failed", "disconnected"].includes(peer.connectionState)) this.retry();
      };
      await peer.setLocalDescription(await peer.createOffer());
      await waitForICE(peer, controller.signal);
      if (!current()) return;
      const response = await fetch(`/api/printers/${encodeURIComponent(this.button.dataset.live)}/webrtc`, {
        method: "POST", signal: controller.signal,
        body: new URLSearchParams({ csrf: this.button.dataset.csrf, sdp: peer.localDescription.sdp }),
      });
      if (response.status === 401 || response.status === 403) {
        this.stop();
        this.status("Page expired", "Reload this page to reconnect.");
        return;
      }
      if (!response.ok) throw new Error("Signaling failed");
      const sdp = await response.text();
      if (current()) await peer.setRemoteDescription({ type: "answer", sdp });
    } catch {
      if (!controller.signal.aborted) this.retry();
    }
  }
}

class LiveSheet {
  constructor(dialog) {
    this.dialog = dialog;
    this.video = dialog.querySelector("video");
    this.title = dialog.querySelector("h2");
    this.status = dialog.querySelector("[data-modal-status]");
    dialog.querySelector("[data-close-live]").addEventListener("click", () => dialog.close());
    dialog.addEventListener("click", (event) => {
      const bounds = dialog.getBoundingClientRect();
      if (event.target === dialog && (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom)) dialog.close();
    });
    dialog.addEventListener("close", () => {
      this.video.srcObject = null;
      document.body.classList.remove("live-sheet-open");
      this.preview?.button.focus();
      this.preview = null;
    });
    this.video.addEventListener("resize", () => this.resize());
    window.addEventListener("resize", () => this.resize());
    window.visualViewport?.addEventListener("resize", () => this.resize());
  }

  open(preview) {
    this.preview = preview;
    this.title.textContent = preview.button.dataset.name;
    if (!this.dialog.open) this.dialog.showModal();
    document.body.classList.add("live-sheet-open");
    this.update(preview);
  }

  update(preview) {
    if (this.preview !== preview) return;
    // Share the existing media; opening the sheet does not create another peer.
    if (this.video.srcObject !== preview.video.srcObject) {
      this.video.srcObject = preview.video.srcObject;
      if (this.video.srcObject) this.video.play().catch(() => {});
    }
    this.status.textContent = preview.message.textContent || "Live";
    this.resize();
  }

  resize() {
    if (!this.dialog.open) return;
    const viewport = window.visualViewport;
    const size = fittedVideoSize(this.video.videoWidth || 640, this.video.videoHeight || 360,
      (viewport?.width || window.innerWidth) - 48, (viewport?.height || window.innerHeight) - 140);
    this.video.style.width = `${size.width}px`;
    this.video.style.height = `${size.height}px`;
  }
}

const liveDialog = document.querySelector("#live-dialog");
if (liveDialog) {
  const sheet = new LiveSheet(liveDialog);
  const previews = Array.from(document.querySelectorAll("[data-live]"), (button) => new LivePreview(button, sheet));
  const resume = () => previews.forEach((preview) => document.hidden ? preview.stop() : preview.connect());
  document.addEventListener("visibilitychange", resume);
  window.addEventListener("pagehide", () => previews.forEach((preview) => preview.stop()));
  window.addEventListener("pageshow", (event) => { if (event.persisted) resume(); });
  resume();
}

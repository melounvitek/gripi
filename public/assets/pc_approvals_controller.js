const COMPUTER_ICON = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><rect x="4.5" y="5" width="15" height="10.5" rx="1.4"></rect><path d="M2.5 19h19"></path></svg>';

// Owns the quiet line above the editor that says, on every device that shows the session, that a computer asks the user
// to approve a request of gripi pc, with Cancel, which refuses it. The editor stays usable, and the line never takes
// its focus or keys.
export class PcApprovalsController {
  constructor(document, window) {
    this.document = document;
    this.window = window;
    this.element = null;
    this.approvals = [];
    this.lines = new Map();
    // Requests that this page cancelled, or found no longer waiting, which a poll from before then may still list.
    this.finished = new Set();
    this.ticker = null;
  }

  bind(element) {
    this.element = element;
    this.lines.clear();
  }

  // Takes the session's requests that computers ask to approve, from its page or its live events. Only the gateway
  // ends them, as the device's clock may be off.
  update(approvals) {
    this.approvals = Array.isArray(approvals) ? approvals : [];
    this.render();
  }

  // Forgets the session before its view is replaced.
  reset() {
    clearInterval(this.ticker);
    this.ticker = null;
    this.approvals = [];
    this.finished.clear();
  }

  render() {
    if (!this.element) return;
    const shown = this.approvals.filter((approval) => !this.finished.has(approval.id));
    for (const [id, line] of this.lines) {
      if (shown.some((approval) => approval.id === id)) continue;
      line.element.remove();
      this.lines.delete(id);
    }
    for (const approval of shown) {
      const line = this.lines.get(approval.id) || this.add(approval);
      const left = Math.max(0, Math.ceil((Date.parse(approval.expires_at) - Date.now()) / 1000));
      line.countdown.textContent = line.failed ? "" : `expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
      line.error.textContent = line.failed ? "could not cancel" : "";
    }
    if (shown.length && !this.ticker) this.ticker = setInterval(() => this.render(), 1000);
    if (!shown.length) {
      clearInterval(this.ticker);
      this.ticker = null;
    }
  }

  add(approval) {
    const element = this.document.createElement("div");
    element.className = "pc-approval";
    const icon = this.document.createElement("span");
    icon.className = "pc-approval-icon";
    icon.innerHTML = COMPUTER_ICON;
    const text = this.document.createElement("span");
    text.className = "pc-approval-text";
    const computer = this.document.createElement("strong");
    computer.textContent = approval.computer || "your computer";
    text.append("Waiting for you on ", computer, `: ${approval.summary}`);
    const time = this.document.createElement("span");
    time.className = "pc-approval-time";
    // The countdown would be read out every second, unlike the error, which the line's live region announces.
    const countdown = this.document.createElement("span");
    countdown.setAttribute("aria-live", "off");
    const error = this.document.createElement("span");
    error.className = "pc-approval-error";
    time.append(countdown, error);
    const cancel = this.document.createElement("button");
    cancel.type = "button";
    cancel.className = "pc-approval-cancel";
    cancel.textContent = "cancel";
    cancel.setAttribute("aria-label", `cancel ${approval.summary}`);
    element.append(icon, text, time, cancel);
    const line = { element, countdown, error, cancel, failed: false };
    cancel.addEventListener("click", () => this.cancel(approval.id, line));
    this.element.append(element);
    this.lines.set(approval.id, line);
    return line;
  }

  async cancel(id, line) {
    if (line.cancel.disabled) return;
    line.cancel.disabled = true;
    line.failed = false;
    let response = null;
    try {
      response = await this.window.fetch("/pc/cancel", { method: "POST", body: new URLSearchParams({ id }), headers: { Accept: "application/json" } });
    } catch (_error) {
    }
    line.cancel.disabled = false;
    // Too late, the request no longer waits for approval, such as once the user approved it on the computer.
    if (response?.ok || response?.status === 409) this.finished.add(id);
    else line.failed = true;
    this.render();
  }
}

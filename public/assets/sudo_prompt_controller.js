// How long the prompt says that its request is gone before it closes.
const GONE_NOTICE_MS = 3000;

// Owns Gripi's own prompt for the password of a 'gripi sudo' request, which takes the editor's place while the session
// has a pending request. The password lives only in the prompt's field, which every way out of the prompt clears.
export class SudoPromptController {
  constructor(window, { canFocus }) {
    this.window = window;
    this.canFocus = canFocus;
    this.elements = null;
    this.requests = [];
    // Requests that this page answered, cancelled or saw expire, which a poll from before then may still list.
    this.finished = new Set();
    this.request = null;
    this.sending = false;
    this.ticker = null;
    this.closing = null;
    // The browser keeps a page for Back and Forward with what its fields hold.
    window.addEventListener("pagehide", () => this.clearPassword());
    window.addEventListener("pageshow", () => this.clearPassword());
  }

  bind(prompt, form) {
    this.elements = prompt && form ? {
      prompt,
      form,
      expiry: prompt.querySelector("[data-sudo-expiry]"),
      command: prompt.querySelector("[data-sudo-command]"),
      cwd: prompt.querySelector("[data-sudo-cwd]"),
      password: prompt.querySelector("[data-sudo-password]"),
      error: prompt.querySelector("[data-sudo-error]")
    } : null;
    if (!this.elements) return;
    prompt.addEventListener("keydown", (event) => this.keydown(event));
    prompt.querySelector("[data-sudo-run]").addEventListener("click", () => this.answer());
    prompt.querySelector("[data-sudo-cancel]").addEventListener("click", () => this.cancel());
  }

  get shown() {
    return !!this.request;
  }

  // Takes the session's pending requests, from its page or its live events, and shows the oldest.
  update(requests) {
    this.requests = Array.isArray(requests) ? requests : [];
    this.render();
  }

  // Forgets the session before its view is replaced.
  reset() {
    this.clearPassword();
    clearInterval(this.ticker);
    clearTimeout(this.closing);
    this.ticker = this.closing = this.request = null;
    this.requests = [];
    this.finished.clear();
    this.sending = false;
  }

  clearPassword() {
    if (this.elements) this.elements.password.value = "";
  }

  render() {
    if (!this.elements || this.closing) return;
    const now = Date.now();
    const request = this.requests.find((each) => !this.finished.has(each.id) && Date.parse(each.expires_at) > now) || null;
    if (request?.id === this.request?.id) return;
    const { prompt, form, command, cwd, password, error } = this.elements;
    // Decided before the editor goes, so that a message being typed doesn't go on in the password field.
    const focus = request && !this.request && this.canFocus();
    this.clearPassword();
    error.hidden = true;
    clearInterval(this.ticker);
    this.ticker = null;
    this.request = request;
    this.sending = false;
    form.toggleAttribute("data-sudo", !!request);
    prompt.hidden = !request;
    // Nothing takes the focus when the prompt goes: what is typed next could be the rest of the password.
    if (!request) return;
    command.textContent = request.display;
    cwd.textContent = request.cwd;
    this.ticker = setInterval(() => this.tick(), 1000);
    this.tick();
    // The first tick may already find it expired and show the next one.
    if (focus && this.request === request) password.focus({ preventScroll: true });
  }

  tick() {
    const left = Math.ceil((Date.parse(this.request.expires_at) - Date.now()) / 1000);
    if (left <= 0) {
      this.finished.add(this.request.id);
      this.render();
      return;
    }
    this.elements.expiry.textContent = `expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
  }

  keydown(event) {
    if (event.isComposing) return;
    if (event.key === "Escape") {
      // Also keeps Escape from stopping the agent.
      event.preventDefault();
      this.cancel();
    } else if (event.key === "Enter" && event.target === this.elements.password) {
      event.preventDefault();
      this.answer();
    }
  }

  answer() {
    const password = this.elements.password.value;
    this.clearPassword();
    return this.send("/sudo/answer", { password });
  }

  cancel() {
    this.clearPassword();
    return this.send("/sudo/cancel", {});
  }

  async send(url, fields) {
    const request = this.request;
    if (!request || this.sending || this.finished.has(request.id)) return;
    this.sending = true;
    this.elements.error.hidden = true;
    let response = null;
    let message = "";
    try {
      // A form body has a known length, unlike a stream, which the gateway refuses for the password.
      response = await this.window.fetch(url, { method: "POST", body: new URLSearchParams({ id: request.id, ...fields }), headers: { Accept: "application/json" } });
      if (!response.ok) message = (await response.json().catch(() => ({}))).error || "";
    } catch (_error) {
    }
    if (this.request !== request) return;
    this.sending = false;
    if (response?.ok) {
      this.finished.add(request.id);
      this.render();
      return;
    }
    const { error } = this.elements;
    error.textContent = message || "Could not answer. Try again.";
    error.hidden = false;
    // Answered elsewhere, cancelled by its command, or expired: the prompt says so before it closes.
    if (response?.status === 409) {
      this.finished.add(request.id);
      this.closing = setTimeout(() => {
        this.closing = null;
        this.render();
      }, GONE_NOTICE_MS);
    }
  }
}

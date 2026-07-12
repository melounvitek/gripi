class AccessRequestController {
  constructor(document, kind) {
    this.document = document;
    this.kind = kind;
    this.currentCode = null;
    this.pollTimer = null;
    this.pollPromise = null;
    this.pollAbortController = null;
    this.pollGeneration = 0;
    this.resolving = false;
    this.active = false;

    document.addEventListener("click", (event) => {
      if (event.target.closest(`[data-${kind}-access-allow]`)) return this.resolve("approve").catch(() => {});
      if (event.target.closest(`[data-${kind}-access-deny]`)) return this.resolve("deny").catch(() => {});
    });
  }

  resume() {
    if (!this.enabled() || this.modalIsOpen()) return Promise.resolve();
    this.active = true;
    if (this.pollTimer || this.pollPromise) return this.pollPromise || Promise.resolve();
    return this.poll();
  }

  pause() {
    this.active = false;
    this.invalidatePoll();
  }

  invalidatePoll() {
    this.pollGeneration += 1;
    clearTimeout(this.pollTimer);
    this.pollTimer = null;
    this.pollAbortController?.abort();
    this.pollAbortController = null;
    this.pollPromise = null;
  }

  async resolve(action) {
    if (!this.currentCode || this.resolving) return;
    this.resolving = true;
    const formData = new FormData();
    formData.set("code", this.currentCode);
    this.invalidatePoll();
    try {
      await fetch(`/${this.kind}-access/${action}`, { method: "POST", body: formData, headers: { "Accept": "application/json" } });
      this.show(null);
    } finally {
      this.resolving = false;
      if (this.active) await this.poll();
    }
  }

  elements() {
    return {
      overlay: this.document.querySelector(`[data-${this.kind}-access-overlay]`),
      title: this.document.querySelector(`[data-${this.kind}-access-title]`),
      meta: this.document.querySelector(`[data-${this.kind}-access-meta]`)
    };
  }

  enabled() {
    return this.document.body.dataset[`${this.kind}AccessEnabled`] === "true";
  }

  modalIsOpen() {
    return !!this.document.querySelector("[data-modal]:not([hidden])");
  }

  poll() {
    if (!this.active || this.resolving || this.modalIsOpen()) return Promise.resolve();
    const generation = this.pollGeneration;
    const abortController = new AbortController();
    this.pollAbortController = abortController;
    const promise = this.fetchPending(generation, abortController);
    this.pollPromise = promise;
    return promise;
  }

  async fetchPending(generation, abortController) {
    try {
      const response = await fetch(`/${this.kind}-access/pending`, { headers: { "Accept": "application/json" }, signal: abortController.signal });
      if (generation !== this.pollGeneration || !this.active) return;
      if (response.ok) {
        const payload = await response.json();
        if (generation !== this.pollGeneration || !this.active) return;
        this.show((payload.requests || [])[0]);
      }
    } catch (_error) {
    } finally {
      if (this.pollAbortController === abortController) this.pollAbortController = null;
      if (generation === this.pollGeneration) this.pollPromise = null;
      if (generation === this.pollGeneration && this.active && !this.modalIsOpen()) {
        this.pollTimer = setTimeout(() => {
          this.pollTimer = null;
          this.poll();
        }, 3000);
      }
    }
  }

  show(request) {
    const elements = this.elements();
    if (!elements.overlay) return;
    if (!request) {
      this.currentCode = null;
      elements.overlay.classList.remove("is-visible");
      return;
    }

    this.currentCode = request.code;
    elements.title.textContent = `New ${this.kind} requests access: ${request.code}`;
    elements.meta.textContent = this.requestMeta(request);
    elements.overlay.classList.add("is-visible");
  }
}

export class BrowserAccessRequestController extends AccessRequestController {
  constructor(document) {
    super(document, "browser");
  }

  requestMeta(request) {
    return [request.ip, request.user_agent].filter(Boolean).join(" · ");
  }
}

export class WorkspaceAccessRequestController extends AccessRequestController {
  constructor(document) {
    super(document, "workspace");
  }

  requestMeta() {
    return "Approve only if a trusted colleague is waiting for this code.";
  }
}

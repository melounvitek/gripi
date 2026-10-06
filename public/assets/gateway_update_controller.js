const PROGRESS_STATES = ["waiting", "updating", "restarting"];

const failedStep = (failure) => `${failure.step} · ${failure.timedOut ? "timed out" : `exit ${failure.exitStatus}`}`;

export class GatewayUpdateController {
  constructor(document, window, callbacks, BroadcastChannelClass = globalThis.BroadcastChannel) {
    this.document = document;
    this.window = window;
    this.callbacks = callbacks;
    this.instanceId = document.body.dataset.gatewayInstanceId;
    this.state = null;
    this.inProgress = false;
    this.pollTimer = null;
    this.checkInterval = null;
    this.overlay = document.querySelector("[data-gateway-update-overlay]");
    this.channel = typeof BroadcastChannelClass === "function" ? new BroadcastChannelClass("gripi-update") : null;

    document.addEventListener("click", (event) => {
      if (event.target.closest("[data-gateway-update-button]")) this.start();
      const copy = event.target.closest("[data-gateway-update-failure-copy]");
      if (copy) this.copyFailure(copy);
    });
    // The open overlay makes the page inert, but shortcuts bound to the document would still act.
    // No preventDefault: browser shortcuts such as reload must keep working.
    window.addEventListener("keydown", (event) => {
      if (this.overlay.open) event.stopImmediatePropagation();
    }, true);
    // For browsers without closedby, where Escape would dismiss the overlay.
    this.overlay.addEventListener("cancel", (event) => event.preventDefault());
    this.channel?.addEventListener("message", (event) => {
      if (event.data?.type !== "updating") return;
      this.inProgress = true;
      this.poll();
    });
    ["pageshow", "focus", "online"].forEach((eventName) => {
      window.addEventListener(eventName, () => this.check({ refresh: true }).catch(() => {}));
    });
    window.addEventListener("visibilitychange", () => {
      if (!document.hidden) this.check({ refresh: true }).catch(() => {});
    });
  }

  apply(payload = this.state) {
    if (!payload) return;
    this.state = payload;
    // Waiting for active sessions leaves the page usable so they can still be watched or aborted.
    const blocking = payload.state === "updating" || payload.state === "restarting";
    const progressMessage = payload.state === "restarting" ? "Restarting Gripi…" : "Updating Gripi…";
    this.overlay.querySelector("[data-gateway-update-overlay-message]").textContent = progressMessage;
    if (blocking && !this.overlay.open) this.overlay.showModal();
    if (!blocking && this.overlay.open) this.overlay.close();
    const control = this.document.querySelector("[data-gateway-update]");
    const button = control?.querySelector("[data-gateway-update-button]");
    const message = control?.querySelector("[data-gateway-update-message]");
    if (!control || !button || !message) return;

    const available = payload.state === "available";
    const progressing = PROGRESS_STATES.includes(payload.state);
    const failed = ["error", "dependency_failed", "rollback_failed"].includes(payload.state);
    // A retry keeps the old payload's failure until the gateway answers.
    const failure = failed ? payload.failure : null;
    control.querySelector("[data-gateway-update-note]").hidden = !failure;
    control.querySelector("[data-gateway-update-details]").hidden = !failure;
    const details = this.document.querySelector('[data-modal="gateway-update-failure-modal"]');
    if (failure) {
      details.querySelector("[data-gateway-update-failure-summary]").textContent = payload.message;
      details.querySelector("[data-gateway-update-failure-note]").textContent = `Nothing was changed. Gripi is still running ${payload.currentSha}.`;
      details.querySelector("[data-gateway-update-failure-step]").textContent = failedStep(failure);
      details.querySelector("[data-gateway-update-failure-output]").textContent = failure.output;
    } else if (!details.hidden) {
      this.callbacks.closeModal(details);
    }
    const retryable = failed && payload.state !== "rollback_failed";
    const blocked = payload.state === "blocked";
    control.hidden = !(available || progressing || failed || blocked);
    control.classList.toggle("is-error", failed || blocked);
    button.hidden = !(available || retryable);
    if (available || retryable) {
      button.textContent = retryable ? "Retry update" : `Update to ${payload.targetSha || "latest"}`;
      button.title = payload.summary || payload.message || "Update Gripi";
      message.textContent = failure ? `Update to ${payload.targetSha} failed.` : payload.message || "Gripi update available";
    } else {
      message.textContent = payload.message || progressMessage;
    }
  }

  async check({ refresh = true } = {}) {
    const url = refresh ? "/gateway-update/check" : "/gateway-update";
    const method = refresh ? "POST" : "GET";
    // The poll must not hang on a restarting gateway.
    const signal = refresh ? undefined : AbortSignal.timeout(10000);
    const response = await fetch(url, { method, headers: { "Accept": "application/json" }, cache: "no-store", signal });
    if (!response.ok) throw new Error("Could not check for Gripi updates");
    const payload = await response.json();
    if (payload.instanceId && payload.instanceId !== this.instanceId) {
      // Stop polling: navigating again would abandon a page load that takes longer than the poll.
      this.inProgress = false;
      this.navigate(payload.currentSha || payload.instanceId);
      return payload;
    }
    // An update started in another window or device needs polling here too, to reload after it.
    this.inProgress = PROGRESS_STATES.includes(payload.state);
    if (this.inProgress) this.poll();
    this.apply(payload);
    return payload;
  }

  async start() {
    const target = this.state?.targetSha || "the latest version";
    if (!this.window.confirm(`Update Gripi to ${target}? Gripi will wait for active Pi work before updating and restarting.`)) return;

    this.inProgress = true;
    this.channel?.postMessage({ type: "updating" });
    // Shown as waiting, which does not block: only the gateway's answer tells whether the page is blocked.
    this.apply({ ...this.state, state: "waiting", message: "Starting Gripi update…" });
    try {
      const response = await fetch("/gateway-update", { method: "POST", headers: { "Accept": "application/json" } });
      if (!response.ok) throw new Error("Could not start Gripi update");
      this.apply(await response.json());
      this.poll();
    } catch (error) {
      this.inProgress = false;
      this.apply({ state: "error", message: error.message });
    }
  }

  async copyFailure(button) {
    const { targetSha, message, failure } = this.state;
    const copied = await this.callbacks.copyText([`Update to ${targetSha} failed`, message, failedStep(failure), failure.output].join("\n")).catch(() => false);
    button.textContent = copied ? "Copied" : "Copy failed";
    setTimeout(() => { button.textContent = "Copy details"; }, 1200);
  }

  resume() {
    if (!this.checkInterval) this.checkInterval = setInterval(() => this.check({ refresh: true }).catch(() => {}), 5 * 60 * 1000);
  }

  cleanNavigation() {
    const cleanUrl = new URL(this.window.location.href);
    if (!cleanUrl.searchParams.has("_gateway_updated")) return;
    cleanUrl.searchParams.delete("_gateway_updated");
    this.window.history.replaceState(this.window.history.state, "", cleanUrl.href);
  }

  poll() {
    clearTimeout(this.pollTimer);
    this.pollTimer = setTimeout(async () => {
      try {
        await this.check({ refresh: false });
      } catch (_error) {
      }
      if (this.inProgress) this.poll();
    }, 1000);
  }

  navigate(targetSha) {
    const cleanUrl = new URL(this.window.location.href);
    cleanUrl.searchParams.delete("_gateway_updated");
    const updateUrl = new URL(cleanUrl.href);
    updateUrl.searchParams.set("_gateway_updated", targetSha);
    this.window.location.replace(updateUrl.href);
  }
}

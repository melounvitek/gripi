const PROGRESS_STATES = ["waiting", "updating", "restarting"];

export class GatewayUpdateController {
  constructor(document, window, BroadcastChannelClass = globalThis.BroadcastChannel) {
    this.document = document;
    this.window = window;
    this.instanceId = document.body.dataset.gatewayInstanceId;
    this.state = null;
    this.inProgress = false;
    this.pollTimer = null;
    this.checkInterval = null;
    this.overlay = document.querySelector("[data-gateway-update-overlay]");
    this.channel = typeof BroadcastChannelClass === "function" ? new BroadcastChannelClass("gripi-update") : null;

    document.addEventListener("click", (event) => {
      if (event.target.closest("[data-gateway-update-button]")) this.start();
    });
    // The open overlay makes the page inert, but shortcuts bound to the document would still act.
    // Browser shortcuts such as reload keep working.
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
    this.overlay.querySelector("[data-gateway-update-overlay-message]").textContent = payload.state === "restarting" ? "Restarting gateway…" : "Updating gateway…";
    if (blocking && !this.overlay.open) this.overlay.showModal();
    if (!blocking && this.overlay.open) this.overlay.close();
    const control = this.document.querySelector("[data-gateway-update]");
    const button = control?.querySelector("[data-gateway-update-button]");
    const message = control?.querySelector("[data-gateway-update-message]");
    if (!control || !button || !message) return;

    const available = payload.state === "available";
    const progressing = PROGRESS_STATES.includes(payload.state);
    const failed = ["error", "dependency_failed", "rollback_failed"].includes(payload.state);
    const retryable = failed && payload.state !== "rollback_failed";
    const blocked = payload.state === "blocked";
    control.hidden = !(available || progressing || failed || blocked);
    control.classList.toggle("is-error", failed || blocked);
    button.hidden = !(available || retryable);
    if (available || retryable) {
      button.textContent = retryable ? "Retry update" : `Update to ${payload.targetSha || "latest"}`;
      button.title = payload.summary || payload.message || "Update gateway";
      message.textContent = payload.message || "Gateway update available";
    } else {
      message.textContent = payload.message || (payload.state === "restarting" ? "Restarting gateway…" : "Updating gateway…");
    }
  }

  async check({ refresh = true } = {}) {
    const url = refresh ? "/gateway-update/check" : "/gateway-update";
    const method = refresh ? "POST" : "GET";
    // The poll is the only way out of the overlay, so it must not hang on a restarting gateway.
    const signal = refresh ? undefined : AbortSignal.timeout(10000);
    const response = await fetch(url, { method, headers: { "Accept": "application/json" }, cache: "no-store", signal });
    if (!response.ok) throw new Error("Could not check for gateway updates");
    const payload = await response.json();
    if (payload.instanceId && payload.instanceId !== this.instanceId) {
      // Stop polling: navigating again would abandon a page load that takes longer than the poll.
      this.inProgress = false;
      this.navigate(payload.currentSha || payload.instanceId);
      return payload;
    }
    // An update started in another window or device needs polling here too, to reload after it.
    const progressing = PROGRESS_STATES.includes(payload.state);
    if (progressing && !this.inProgress) this.poll();
    this.inProgress = progressing;
    this.apply(payload);
    return payload;
  }

  async start() {
    const target = this.state?.targetSha || "the latest version";
    if (!this.window.confirm(`Update gateway to ${target}? The gateway will wait for active Pi work before updating and restarting.`)) return;

    this.inProgress = true;
    this.channel?.postMessage({ type: "updating" });
    // Shown as waiting, which does not block: only the gateway's answer tells whether it has to.
    this.apply({ ...this.state, state: "waiting", message: "Starting gateway update…" });
    try {
      const response = await fetch("/gateway-update", { method: "POST", headers: { "Accept": "application/json" } });
      if (!response.ok) throw new Error("Could not start gateway update");
      this.apply(await response.json());
      this.poll();
    } catch (error) {
      this.inProgress = false;
      this.apply({ state: "error", message: error.message });
    }
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

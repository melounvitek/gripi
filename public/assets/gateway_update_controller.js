export class GatewayUpdateController {
  constructor(document, window, BroadcastChannelClass = globalThis.BroadcastChannel) {
    this.document = document;
    this.window = window;
    this.instanceId = document.body.dataset.gatewayInstanceId;
    this.state = null;
    this.inProgress = false;
    this.pollTimer = null;
    this.checkInterval = null;
    this.channel = typeof BroadcastChannelClass === "function" ? new BroadcastChannelClass("gripi-update") : null;

    document.addEventListener("click", (event) => {
      if (event.target.closest("[data-gateway-update-button]")) this.start();
    });
    this.channel?.addEventListener("message", (event) => {
      if (event.data?.type !== "updating") return;
      this.inProgress = true;
      this.poll();
    });
    ["pageshow", "focus", "online"].forEach((eventName) => {
      window.addEventListener(eventName, () => this.check().catch(() => {}));
    });
    window.addEventListener("visibilitychange", () => {
      if (!document.hidden) this.check().catch(() => {});
    });
  }

  apply(payload = this.state) {
    if (!payload) return;
    this.state = payload;
    const control = this.document.querySelector("[data-gateway-update]");
    const button = control?.querySelector("[data-gateway-update-button]");
    const message = control?.querySelector("[data-gateway-update-message]");
    if (!control || !button || !message) return;

    const available = payload.state === "available";
    const progressing = ["updating", "restarting"].includes(payload.state);
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

  async check() {
    const response = await fetch("/gateway-update", { headers: { "Accept": "application/json" }, cache: "no-store" });
    if (!response.ok) throw new Error("Could not check for gateway updates");
    const payload = await response.json();
    if (payload.instanceId && payload.instanceId !== this.instanceId) {
      this.navigate(payload.currentSha || payload.instanceId);
      return payload;
    }
    if (this.inProgress && !["updating", "restarting"].includes(payload.state)) this.inProgress = false;
    this.apply(payload);
    return payload;
  }

  async start() {
    const target = this.state?.targetSha || "the latest version";
    if (!this.window.confirm(`Update gateway to ${target}? Active Pi work will be interrupted while the gateway restarts.`)) return;

    this.inProgress = true;
    this.channel?.postMessage({ type: "updating" });
    this.apply({ ...this.state, state: "updating", message: "Updating gateway…" });
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
    if (!this.checkInterval) this.checkInterval = setInterval(() => this.check().catch(() => {}), 5 * 60 * 1000);
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
        await this.check();
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

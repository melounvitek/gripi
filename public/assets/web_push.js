const NOTIFICATIONS_DISABLED_KEY = "gripi:notifications-disabled";

export class WebPushController {
  constructor(windowObject = window, navigatorObject = navigator, fetchFunction = windowObject.fetch.bind(windowObject)) {
    this.window = windowObject;
    this.navigator = navigatorObject;
    this.fetch = fetchFunction;
    this.preparation = null;
    this.active = false;
    this.generation = 0;
    this.mutations = Promise.resolve();
  }

  available() {
    return "Notification" in this.window && "serviceWorker" in this.navigator && "PushManager" in this.window;
  }

  enabled() {
    return this.active;
  }

  prepare() {
    if (!this.available()) return Promise.resolve(null);
    if (this.preparation) return this.preparation;

    const attempt = Promise.all([
      this.navigator.serviceWorker.register("/service-worker.js").then(() => this.navigator.serviceWorker.ready),
      this.fetchJSON("/web-push/config")
    ]).then(([registration, config]) => ({ registration, publicKey: config.public_key }));
    const retryable = attempt.catch((error) => {
      if (this.preparation === retryable) this.preparation = null;
      throw error;
    });
    this.preparation = retryable;
    return retryable;
  }

  reconcile() {
    const generation = ++this.generation;
    return this.mutate(() => this.reconcileGeneration(generation));
  }

  async reconcileGeneration(generation) {
    if (!this.available() || this.disabled() || this.window.Notification.permission !== "granted") {
      if (generation === this.generation) this.active = false;
      return false;
    }

    const prepared = await this.prepare();
    if (generation !== this.generation) return false;
    const subscription = await this.currentOrNewSubscription(prepared);
    if (generation !== this.generation) {
      if (this.disabled()) await this.remove(subscription).catch(() => {});
      return false;
    }
    await this.store(subscription);
    if (generation !== this.generation) {
      if (this.disabled()) await this.remove(subscription).catch(() => {});
      return false;
    }
    this.active = true;
    return true;
  }

  async enable() {
    if (!this.available()) return false;

    const generation = ++this.generation;
    this.window.localStorage.removeItem(NOTIFICATIONS_DISABLED_KEY);
    const permission = this.window.Notification.permission === "default"
      ? await this.window.Notification.requestPermission()
      : this.window.Notification.permission;
    if (permission !== "granted" || generation !== this.generation) {
      if (generation === this.generation) this.active = false;
      return false;
    }

    return this.mutate(() => this.reconcileGeneration(generation));
  }

  async disable() {
    ++this.generation;
    this.window.localStorage.setItem(NOTIFICATIONS_DISABLED_KEY, "true");
    this.active = false;
    if (!this.available()) return;

    return this.mutate(async () => {
      let registration = null;
      try {
        registration = await this.navigator.serviceWorker.getRegistration("/");
      } catch (_error) {
        return;
      }
      const subscription = await registration?.pushManager?.getSubscription();
      if (!subscription) return;
      await this.remove(subscription);
    });
  }

  mutate(operation) {
    const result = this.mutations.catch(() => {}).then(operation);
    this.mutations = result.catch(() => {});
    return result;
  }

  disabled() {
    return this.window.localStorage.getItem(NOTIFICATIONS_DISABLED_KEY) === "true";
  }

  async currentOrNewSubscription({ registration, publicKey }) {
    let subscription = await registration.pushManager.getSubscription();
    const applicationServerKey = decodeApplicationServerKey(publicKey);
    if (subscription && !subscriptionUsesKey(subscription, applicationServerKey)) {
      await subscription.unsubscribe();
      subscription = null;
    }
    subscription ||= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey });
    return subscription;
  }

  async remove(subscription) {
    try {
      await this.fetchOK("/web-push/subscription", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ endpoint: subscription.endpoint })
      });
    } finally {
      await subscription.unsubscribe();
    }
  }

  async store(subscription) {
    await this.fetchOK("/web-push/subscription", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(subscription.toJSON())
    });
  }

  async fetchJSON(url) {
    const response = await this.fetch(url, { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Web Push setup failed (${response.status})`);
    return response.json();
  }

  async fetchOK(url, options) {
    const response = await this.fetch(url, options);
    if (!response.ok) throw new Error(`Web Push request failed (${response.status})`);
    return response;
  }
}

export function decodeApplicationServerKey(value) {
  const normalized = String(value || "").replace(/-/g, "+").replace(/_/g, "/");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  const decoded = atob(padded);
  return Uint8Array.from(decoded, (character) => character.charCodeAt(0));
}

function subscriptionUsesKey(subscription, expected) {
  const configured = subscription.options?.applicationServerKey;
  if (!configured) return true;
  const actual = new Uint8Array(configured);
  return actual.length === expected.length && actual.every((value, index) => value === expected[index]);
}

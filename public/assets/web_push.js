const NOTIFICATIONS_DISABLED_KEY = "gripi:notifications-disabled";

export class WebPushController {
  constructor(windowObject = window, navigatorObject = navigator, fetchFunction = windowObject.fetch.bind(windowObject)) {
    this.window = windowObject;
    this.navigator = navigatorObject;
    this.fetch = fetchFunction;
    this.preparation = null;
    this.active = false;
  }

  available() {
    return "Notification" in this.window && "serviceWorker" in this.navigator && "PushManager" in this.window;
  }

  enabled() {
    return this.active;
  }

  prepare() {
    if (!this.available()) return Promise.resolve(null);
    this.preparation ||= Promise.all([
      this.navigator.serviceWorker.register("/service-worker.js").then(() => this.navigator.serviceWorker.ready),
      this.fetchJSON("/web-push/config")
    ]).then(([registration, config]) => ({ registration, publicKey: config.public_key }));
    return this.preparation;
  }

  async reconcile() {
    if (!this.available() || this.disabled() || this.window.Notification.permission !== "granted") {
      this.active = false;
      return false;
    }

    const prepared = await this.prepare();
    const subscription = await this.currentOrNewSubscription(prepared);
    await this.store(subscription);
    this.active = true;
    return true;
  }

  async enable() {
    if (!this.available()) return false;

    this.window.localStorage.removeItem(NOTIFICATIONS_DISABLED_KEY);
    const permission = this.window.Notification.permission === "default"
      ? await this.window.Notification.requestPermission()
      : this.window.Notification.permission;
    if (permission !== "granted") {
      this.active = false;
      return false;
    }

    return this.reconcile();
  }

  async disable() {
    this.window.localStorage.setItem(NOTIFICATIONS_DISABLED_KEY, "true");
    this.active = false;
    if (!this.available()) return;

    let registration = null;
    try {
      registration = await this.navigator.serviceWorker.getRegistration("/");
    } catch (_error) {
      return;
    }
    const subscription = await registration?.pushManager?.getSubscription();
    if (!subscription) return;

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

const NOTIFICATION_PRESENCE_CLIENT_KEY = "gripi:notification-presence-client";
const NOTIFICATION_PRESENCE_HEARTBEAT_MS = 10_000;

export class NotificationPresenceController {
  constructor(documentObject, windowObject, sessionPath, fetchFunction = windowObject.fetch.bind(windowObject), clientID = notificationPresenceClientID(windowObject)) {
    this.document = documentObject;
    this.window = windowObject;
    this.sessionPath = sessionPath;
    this.fetch = fetchFunction;
    this.clientID = clientID;
    this.started = false;
    this.lastSession = null;
    this.lastFocused = null;
  }

  start() {
    if (this.started) return;
    this.started = true;

    this.document.addEventListener("visibilitychange", () => this.reportCurrent(true));
    ["focus", "blur", "pageshow", "online"].forEach((eventName) => {
      this.window.addEventListener(eventName, () => this.reportCurrent(true));
    });
    this.window.addEventListener("pagehide", () => this.report("", false, true));
    this.window.setInterval(() => {
      if (this.current().focused) this.reportCurrent(true);
    }, NOTIFICATION_PRESENCE_HEARTBEAT_MS);
    this.reportCurrent(true);
  }

  sessionChanged() {
    this.reportCurrent();
  }

  reportCurrent(force = false) {
    const current = this.current();
    if (!force && current.session === this.lastSession && current.focused === this.lastFocused) return;
    this.report(current.session, current.focused);
  }

  current() {
    const session = String(this.sessionPath() || "");
    const focused = session !== "" && !this.document.hidden && (this.document.hasFocus?.() ?? true);
    return { session: focused ? session : "", focused };
  }

  report(session, focused, keepalive = false) {
    this.lastSession = session;
    this.lastFocused = focused;
    this.fetch("/web-push/presence", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ client_id: this.clientID, session, focused }),
      keepalive
    }).catch(() => {});
  }
}

function notificationPresenceClientID(windowObject) {
  try {
    const existing = windowObject.sessionStorage.getItem(NOTIFICATION_PRESENCE_CLIENT_KEY);
    if (existing) return existing;
    const generated = randomClientID(windowObject);
    windowObject.sessionStorage.setItem(NOTIFICATION_PRESENCE_CLIENT_KEY, generated);
    return generated;
  } catch (_error) {
    return randomClientID(windowObject);
  }
}

function randomClientID(windowObject) {
  if (windowObject.crypto?.randomUUID) return windowObject.crypto.randomUUID();
  const bytes = new Uint8Array(16);
  windowObject.crypto?.getRandomValues?.(bytes);
  if (bytes.some((value) => value !== 0)) {
    return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join("");
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

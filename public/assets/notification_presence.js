const NOTIFICATION_PRESENCE_CLIENT_KEY = "gripi:notification-presence-client";
const NOTIFICATION_PRESENCE_SEQUENCE_KEY = "gripi:notification-presence-sequence";
const NOTIFICATION_PRESENCE_HEARTBEAT_MS = 10_000;

export class NotificationPresenceController {
  // A page in a frame shares sessionStorage with the page around it, so it keeps its identity under its own scope.
  constructor(documentObject, windowObject, sessionPath, fetchFunction = windowObject.fetch.bind(windowObject), clientID = null, storageScope = "", lookedAt = () => !documentObject.hidden && (documentObject.hasFocus?.() ?? true)) {
    this.document = documentObject;
    this.window = windowObject;
    this.sessionPath = sessionPath;
    this.lookedAt = lookedAt;
    this.fetch = fetchFunction;
    this.sequenceKey = `${NOTIFICATION_PRESENCE_SEQUENCE_KEY}${storageScope}`;
    this.clientID = clientID ?? notificationPresenceClientID(windowObject, `${NOTIFICATION_PRESENCE_CLIENT_KEY}${storageScope}`);
    this.sequence = notificationPresenceSequence(windowObject, this.sequenceKey);
    this.started = false;
    this.lastSession = null;
    this.lastFocused = null;
    this.calling = false;
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

  // A voice call is in its session as much as looking at it, also with the screen off or the window in the background.
  callChanged(calling) {
    this.calling = calling;
    this.reportCurrent();
  }

  reportCurrent(force = false) {
    const current = this.current();
    if (!force && current.session === this.lastSession && current.focused === this.lastFocused) return;
    this.report(current.session, current.focused);
  }

  current() {
    const session = String(this.sessionPath() || "");
    const focused = session !== "" && (this.calling || this.lookedAt());
    return { session: focused ? session : "", focused };
  }

  report(session, focused, keepalive = false) {
    this.lastSession = session;
    this.lastFocused = focused;
    this.sequence += 1;
    storeNotificationPresenceSequence(this.window, this.sequenceKey, this.sequence);
    this.fetch("/web-push/presence", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ client_id: this.clientID, session, focused, sequence: this.sequence }),
      keepalive
    }).catch(() => {});
  }
}

function notificationPresenceClientID(windowObject, key) {
  try {
    const existing = windowObject.sessionStorage.getItem(key);
    if (/^[A-Za-z0-9_-]{1,64}$/.test(existing || "")) return existing;
    const generated = randomClientID(windowObject);
    windowObject.sessionStorage.setItem(key, generated);
    return generated;
  } catch (_error) {
    return randomClientID(windowObject);
  }
}

function notificationPresenceSequence(windowObject, key) {
  try {
    const sequence = Number(windowObject.sessionStorage.getItem(key));
    return Number.isSafeInteger(sequence) && sequence >= 0 ? sequence : 0;
  } catch (_error) {
    return 0;
  }
}

function storeNotificationPresenceSequence(windowObject, key, sequence) {
  try {
    windowObject.sessionStorage.setItem(key, String(sequence));
  } catch (_error) {
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

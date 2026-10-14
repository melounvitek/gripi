const HERMES_PANEL_OPEN_KEY = "gripi:hermes-panel-open";
const HERMES_FOLLOW_INTERVAL_MS = 5_000;
const HERMES_WINDOW_FEATURES = "popup,width=480,height=820";

// The Hermes panel shows the page of the current Hermes conversation in a frame next to whatever the page shows, so
// switching sessions never touches it. On that page, the controller keeps to the current conversation, which a new one
// in any window replaces.
export class HermesPanelController {
  constructor(documentObject, windowObject, currentSessionPath, closeSidebar) {
    this.document = documentObject;
    this.window = windowObject;
    this.currentSessionPath = currentSessionPath;
    this.closeSidebar = closeSidebar;
    this.panelPage = documentObject.body.classList.contains("hermes-panel-page");
    this.panel = null;
    this.frame = null;
    this.popup = null;
  }

  initialize() {
    if (this.panelPage) this.initializePanelPage();
    else this.initializePanel();
  }

  initializePanel() {
    this.panel = this.document.getElementById("hermes-panel");
    if (!this.panel) return;
    this.frame = this.panel.querySelector("iframe");
    // Popping out leaves the frame blank, which isn't the conversation either.
    this.frame.addEventListener("load", () => {
      if (this.frame.getAttribute("src") === "/hermes") this.panel.classList.remove("is-loading");
    });
    this.document.addEventListener("click", (event) => {
      if (event.target.closest("[data-hermes-toggle]")) this.toggle();
    });
    this.window.addEventListener("message", (event) => {
      if (event.origin !== this.window.location.origin || event.source !== this.frame.contentWindow) return;
      if (event.data === "gripi:hermes-close") this.close();
      if (event.data === "gripi:hermes-pop-out") this.popOut();
      if (event.data === "gripi:hermes-replied" && this.panel.hidden) this.document.body.classList.add("hermes-replied");
    });
    if (this.storedOpen()) this.open();
  }

  sync() {
    this.document.querySelector("[data-hermes-toggle]")?.setAttribute("aria-expanded", String(!this.panel.hidden));
  }

  toggle() {
    if (this.popup && !this.popup.closed) {
      this.popup.focus();
    } else if (this.panel.hidden) {
      this.open();
    } else {
      this.close();
    }
  }

  open() {
    if (this.frame.getAttribute("src") !== "/hermes") {
      this.panel.classList.add("is-loading");
      this.frame.src = "/hermes";
    }
    this.panel.hidden = false;
    // On a phone, the panel covers the sidebar it opened from, which closing it shouldn't bring back.
    this.closeSidebar();
    this.document.body.classList.add("hermes-panel-open");
    this.document.body.classList.remove("hermes-replied");
    this.storeOpen(true);
    this.sync();
  }

  // Closing only hides the panel, so a reply or a voice call carries on. Focus leaves it, or its hidden page would
  // still count as looked at, which keeps Hermes's replies from notifying. It goes to the Hermes button, or to the
  // conversation while the sidebar is hidden or a phone's sidebar is closed, off the screen.
  close() {
    this.panel.hidden = true;
    this.document.body.classList.remove("hermes-panel-open");
    this.storeOpen(false);
    this.sync();
    const toggle = this.document.querySelector("[data-hermes-toggle]");
    const box = toggle?.getBoundingClientRect();
    const shown = box && box.width > 0 && box.right > 0 && box.left < this.window.innerWidth;
    (shown ? toggle : this.document.getElementById("conversation-scroll"))?.focus({ preventScroll: true });
  }

  popOut() {
    this.popup = this.window.open("/hermes", "gripi-hermes", HERMES_WINDOW_FEATURES);
    // A browser that blocked the window says so itself. The desktop app's window.open returns no window, though it opens one.
    if (!this.popup && !this.window.gripiElectron) return;
    // Without noopener the window starts with a copy of this window's sessionStorage, which holds its notification
    // identity.
    try { this.popup?.sessionStorage.clear(); } catch (_error) {}
    // The window shows the conversation from now on, so the panel stops showing it too.
    this.frame.src = "about:blank";
    this.close();
  }

  storedOpen() {
    try {
      return this.window.localStorage.getItem(HERMES_PANEL_OPEN_KEY) === "true";
    } catch (_error) {
      return false;
    }
  }

  storeOpen(open) {
    try {
      if (open) this.window.localStorage.setItem(HERMES_PANEL_OPEN_KEY, "true");
      else this.window.localStorage.removeItem(HERMES_PANEL_OPEN_KEY);
    } catch (_error) {
    }
  }

  initializePanelPage() {
    // Only the panel itself closes or opens in its own window.
    if (this.window.parent !== this.window) this.document.body.classList.add("hermes-panel-framed");
    this.document.addEventListener("click", (event) => {
      if (event.target.closest("[data-hermes-close]")) this.tellPanel("gripi:hermes-close");
      if (event.target.closest("[data-hermes-pop-out]")) this.tellPanel("gripi:hermes-pop-out");
    });
    const follow = () => this.followCurrentConversation().catch(() => {});
    this.window.addEventListener("focus", follow);
    this.document.addEventListener("visibilitychange", follow);
    this.window.setInterval(follow, HERMES_FOLLOW_INTERVAL_MS);
  }

  replied() {
    if (this.panelPage) this.tellPanel("gripi:hermes-replied");
  }

  tellPanel(message) {
    if (this.window.parent !== this.window) this.window.parent.postMessage(message, this.window.location.origin);
  }

  async followCurrentConversation() {
    if (this.document.hidden) return;
    const response = await this.window.fetch("/hermes/current", { headers: { Accept: "application/json" } });
    if (!response.ok) return;
    const { session } = await response.json();
    if (session !== this.currentSessionPath()) this.window.location.replace("/hermes");
  }
}

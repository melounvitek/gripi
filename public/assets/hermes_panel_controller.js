const HERMES_PANEL_OPEN_KEY = "gripi:hermes-panel-open";
const HERMES_FOLLOW_INTERVAL_MS = 5_000;
const HERMES_WINDOW_FEATURES = "popup,width=480,height=820";

// The Hermes panel shows the page of the current Hermes conversation in a frame next to whatever the page shows, so
// switching sessions never touches it. On that page, the controller keeps to the current conversation, which a new one
// in any window replaces.
export class HermesPanelController {
  constructor(documentObject, windowObject, currentSessionPath, toggleCommandPalette, focusConversation) {
    this.document = documentObject;
    this.window = windowObject;
    this.currentSessionPath = currentSessionPath;
    this.toggleCommandPalette = toggleCommandPalette;
    this.focusConversation = focusConversation;
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
      if (this.frame.getAttribute("src") !== "/hermes") return;
      this.panel.classList.remove("is-loading");
      if (this.focusOnLoad) this.focusFrame();
    });
    this.document.addEventListener("click", (event) => {
      if (event.target.closest("[data-hermes-toggle]")) this.toggle();
    });
    // Hermes loading takes the cursor only if you haven't put it elsewhere meanwhile, such as into the palette.
    this.document.addEventListener("focusin", () => { this.focusOnLoad = false; });
    this.window.addEventListener("message", (event) => {
      if (event.origin !== this.window.location.origin || event.source !== this.frame.contentWindow) return;
      if (event.data === "gripi:hermes-close") this.close();
      if (event.data === "gripi:hermes-pop-out") this.popOut();
      if (event.data === "gripi:hermes-command-palette") this.toggleCommandPalette();
      if (event.data === "gripi:hermes-replied" && this.panel.hidden) {
        this.document.body.classList.add("hermes-replied");
        this.sync();
      }
    });
    if (this.storedOpen()) this.open();
  }

  sync() {
    const toggle = this.document.querySelector("[data-hermes-toggle]");
    toggle.setAttribute("aria-expanded", String(!this.panel.hidden));
    toggle.title = this.document.body.classList.contains("hermes-replied") ? "Hermes replied" : "Hermes";
  }

  toggle() {
    if (this.popup && !this.popup.closed) {
      this.popup.focus();
    } else if (this.panel.hidden) {
      this.open();
      this.focusOnLoad = true;
      if (!this.panel.classList.contains("is-loading")) this.focusFrame();
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
    this.document.body.classList.add("hermes-panel-open");
    this.document.body.classList.remove("hermes-replied");
    this.storeOpen(true);
    this.sync();
  }

  // Hermes has the cursor, or takes it once it loads.
  keepsFocus() {
    return this.focusOnLoad || this.document.activeElement === this.frame;
  }

  // The panel's page puts the cursor in Hermes's conversation, which the page itself never takes from the page around it.
  focusFrame() {
    this.focusOnLoad = false;
    this.frame.contentWindow.postMessage("gripi:hermes-focus", this.window.location.origin);
  }

  // Closing only hides the panel, so a reply or a voice call carries on. Focus goes back to the session, or the hidden
  // page would still count as looked at, which keeps Hermes's replies from notifying.
  close() {
    this.panel.hidden = true;
    this.focusOnLoad = false;
    this.document.body.classList.remove("hermes-panel-open");
    this.storeOpen(false);
    this.sync();
    this.focusConversation();
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
    this.window.addEventListener("message", (event) => {
      if (event.origin === this.window.location.origin && event.source === this.window.parent && event.data === "gripi:hermes-focus") this.focusConversation();
    });
    const follow = () => this.followCurrentConversation().catch(() => {});
    this.window.addEventListener("focus", follow);
    this.document.addEventListener("visibilitychange", follow);
    this.window.setInterval(follow, HERMES_FOLLOW_INTERVAL_MS);
  }

  // The panel's page has no palette, so Ctrl+K there opens the palette of the page around it, which can hide the panel.
  requestCommandPalette() {
    if (!this.document.body.classList.contains("hermes-panel-framed")) return false;
    this.tellPanel("gripi:hermes-command-palette");
    return true;
  }

  // Ctrl+H shows or hides the panel, also from its page inside it, once for a held key.
  toggleFromShortcut(repeat) {
    const framed = this.document.body.classList.contains("hermes-panel-framed");
    if (!this.panel && !framed) return false;
    if (repeat) return true;
    if (this.panel) this.toggle();
    else this.tellPanel("gripi:hermes-close");
    return true;
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

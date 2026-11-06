const HERMES_PANEL_OPEN_KEY = "gripi:hermes-panel-open";
const HERMES_PANEL_WIDTH_KEY = "gripi:hermes-panel-width";
// How far the Hermes tab moves before a press on it resizes the panel rather than closes it.
const HERMES_RESIZE_THRESHOLD_PX = 4;
const HERMES_FOLLOW_INTERVAL_MS = 5_000;
const HERMES_WINDOW_FEATURES = "popup,width=480,height=820";

// The Hermes panel shows the page of the current Hermes conversation in a frame next to whatever the page shows, so
// switching sessions never touches it. On that page, the controller keeps to the current conversation, which a new one
// in any window replaces.
export class HermesPanelController {
  constructor(documentObject, windowObject, currentSessionPath, toggleCommandPalette, focusConversation, runVoiceShortcut, lookChanged) {
    this.document = documentObject;
    this.window = windowObject;
    this.currentSessionPath = currentSessionPath;
    this.toggleCommandPalette = toggleCommandPalette;
    this.focusConversation = focusConversation;
    this.runVoiceShortcut = runVoiceShortcut;
    this.lookChanged = lookChanged;
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
      this.tellFrameLookChanged();
      if (this.focusOnLoad) this.focusFrame();
      if (this.callOnLoad) this.frame.contentWindow.postMessage("gripi:hermes-voice-call", this.window.location.origin);
      this.callOnLoad = false;
    });
    this.showWidth(this.stored(HERMES_PANEL_WIDTH_KEY));
    this.document.querySelector("[data-hermes-toggle]").addEventListener("pointerdown", (event) => this.startResize(event));
    this.document.addEventListener("click", (event) => {
      // The click that ends a drag only ends it.
      if (event.target.closest("[data-hermes-toggle]")) {
        if (this.resized) this.resized = false;
        else this.toggle();
      }
      // On a phone, the panel covers the tab, so its loading screen needs a way out.
      if (event.target.closest("[data-hermes-panel-close]")) this.close();
    });
    // Hermes loading takes the cursor only if you haven't put it elsewhere meanwhile, such as into the palette.
    this.document.addEventListener("focusin", () => { this.focusOnLoad = false; });
    // The panel's page is looked at while this window has focus, which its own focus events miss with the cursor here.
    this.window.addEventListener("focus", () => this.tellFrameLookChanged());
    this.window.addEventListener("blur", () => this.tellFrameLookChanged());
    this.window.addEventListener("message", (event) => {
      if (event.origin !== this.window.location.origin || event.source !== this.frame.contentWindow) return;
      if (event.data === "gripi:hermes-close") this.close();
      if (event.data === "gripi:hermes-pop-out") this.popOut();
      if (event.data === "gripi:hermes-command-palette") this.toggleCommandPalette();
      if (event.data === "gripi:hermes-focus-session") this.focusConversation();
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
      this.focusPanel();
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
    this.tellFrameLookChanged();
  }

  // Hermes has the cursor, or takes it once it loads.
  keepsFocus() {
    return this.focusOnLoad || this.document.activeElement === this.frame;
  }

  focusPanel() {
    this.focusOnLoad = true;
    if (!this.panel.classList.contains("is-loading")) this.focusFrame();
  }

  // The panel's page puts the cursor in Hermes's conversation, which the page itself never takes from the page around it.
  focusFrame() {
    this.focusOnLoad = false;
    this.frame.contentWindow.postMessage("gripi:hermes-focus", this.window.location.origin);
  }

  // Closing only hides the panel, so a reply or a voice call carries on, and notifies like one in a session you've left.
  // Focus goes back to the session.
  close() {
    this.panel.hidden = true;
    this.focusOnLoad = false;
    this.callOnLoad = false;
    this.document.body.classList.remove("hermes-panel-open");
    this.storeOpen(false);
    this.sync();
    this.tellFrameLookChanged();
    this.focusConversation();
  }

  tellFrameLookChanged() {
    this.frame.contentWindow?.postMessage("gripi:hermes-look-changed", this.window.location.origin);
  }

  // A page is looked at while its window shows it and has focus. Framed, the panel's page is looked at while the panel
  // shows it next to the session, with the cursor on either side, so its replies count as read rather than notify. Until
  // the page has loaded, the panel shows that Hermes is starting instead.
  lookedAt() {
    if (this.document.hidden) return false;
    if (!this.document.body.classList.contains("hermes-panel-framed")) return this.document.hasFocus();
    const panel = this.window.frameElement.closest("#hermes-panel");
    return !panel.hidden && !panel.classList.contains("is-loading") && this.window.parent.document.hasFocus();
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

  // Dragging the tab of the open panel sideways resizes the panel, within the bounds that the stylesheet sets.
  startResize(event) {
    this.resized = false;
    if (this.panel.hidden || event.button !== 0) return;
    const tab = event.currentTarget;
    const startX = event.clientX;
    const startWidth = this.panel.getBoundingClientRect().width;
    // The tab keeps the pointer while it crosses the panel's frame, which would otherwise take it.
    tab.setPointerCapture(event.pointerId);
    const move = (moveEvent) => {
      const by = startX - moveEvent.clientX;
      if (!this.resized && Math.abs(by) < HERMES_RESIZE_THRESHOLD_PX) return;
      this.resized = true;
      this.showWidth(`${startWidth + by}px`);
    };
    const end = () => {
      tab.removeEventListener("pointermove", move);
      tab.removeEventListener("pointerup", end);
      tab.removeEventListener("pointercancel", end);
      // The width the panel shows, so a drag past a bound leaves none of the way back to drag over again.
      if (!this.resized) return;
      const width = `${this.panel.getBoundingClientRect().width}px`;
      this.showWidth(width);
      this.store(HERMES_PANEL_WIDTH_KEY, width);
    };
    tab.addEventListener("pointermove", move);
    tab.addEventListener("pointerup", end);
    tab.addEventListener("pointercancel", end);
  }

  showWidth(width) {
    if (width) this.document.documentElement.style.setProperty("--hermes-panel-width", width);
  }

  stored(key) {
    try {
      return this.window.localStorage.getItem(key);
    } catch (_error) {
      return null;
    }
  }

  store(key, value) {
    try {
      this.window.localStorage.setItem(key, value);
    } catch (_error) {
    }
  }

  storedOpen() {
    return this.stored(HERMES_PANEL_OPEN_KEY) === "true";
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
      if (event.origin !== this.window.location.origin || event.source !== this.window.parent) return;
      if (event.data === "gripi:hermes-focus") this.focusConversation();
      if (event.data === "gripi:hermes-voice-call") this.runVoiceShortcut("call");
      if (event.data === "gripi:hermes-voice-mute") this.runVoiceShortcut("mute");
      if (event.data === "gripi:hermes-look-changed") this.lookChanged();
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

  // With the panel shown, Tab goes on from either side's conversation to the other side's composer.
  focusOtherSide() {
    if (this.panel && !this.panel.hidden) this.focusPanel();
    else if (this.document.body.classList.contains("hermes-panel-framed")) this.tellPanel("gripi:hermes-focus-session");
    else return false;
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

  // Ctrl+Shift+H and Ctrl+Shift+M reach Hermes's voice call on its page, and around the panel while it shows.
  voiceShortcut(action, repeat) {
    const shown = this.panel && !this.panel.hidden;
    if (!shown && !this.panelPage) return false;
    if (repeat) return true;
    if (!shown) {
      this.runVoiceShortcut(action);
    } else if (this.panel.classList.contains("is-loading")) {
      // Hermes starting has no call to mute yet.
      if (action === "call") this.callOnLoad = !this.callOnLoad;
    } else {
      this.frame.contentWindow.postMessage(`gripi:hermes-voice-${action}`, this.window.location.origin);
    }
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

// The scratch overlay shows the page of the user's scratch session, a Pi session that Gripi keeps nothing of, in a
// frame over the page, which stays as it is. On that page, the controller passes on to the overlay what the page's
// buttons, keys and links to sessions ask for.
export class ScratchController {
  constructor(documentObject, windowObject, focusConversation, openSessionLink) {
    this.document = documentObject;
    this.window = windowObject;
    this.focusConversation = focusConversation;
    this.openSessionLink = openSessionLink;
    this.page = documentObject.body.classList.contains("scratch-page");
    this.overlay = null;
    this.frame = null;
    this.returnFocus = null;
    this.working = false;
  }

  initialize() {
    if (this.page) this.initializePage();
    else this.initializeOverlay();
  }

  initializeOverlay() {
    this.overlay = this.document.querySelector("[data-scratch-overlay]");
    if (!this.overlay) return;
    this.frame = this.overlay.querySelector("iframe");
    this.frame.addEventListener("load", () => {
      const source = this.frame.getAttribute("src");
      if (!source || source === "about:blank") return;
      this.overlay.classList.remove("is-loading");
      if (this.shown()) this.focusFrame();
    });
    // The sidebar, with its button, is rendered again as it refreshes.
    this.document.addEventListener("click", (event) => {
      if (event.target.closest("[data-scratch-toggle]")) this.toggle();
      else if (event.target === this.overlay) this.hide();
    });
    this.window.addEventListener("message", (event) => {
      if (event.origin !== this.window.location.origin || event.source !== this.frame.contentWindow) return;
      if (event.data === "gripi:scratch-hide") this.hide();
      if (event.data === "gripi:scratch-discarded") this.discarded();
      if (event.data === "gripi:scratch-new-session") this.requestNewSession();
      if (event.data === "gripi:scratch-working") this.markWorking(true);
      if (event.data === "gripi:scratch-idle") this.markWorking(false);
      if (event.data === "gripi:scratch-gone") this.clearActivity();
      // The session opens in the page, and the scratch session waits behind it, as for its next question.
      if (event.data?.openSession) {
        this.hide();
        this.openSessionLink(event.data.openSession);
      }
    });
  }

  shown() {
    return !!this.overlay && !this.overlay.hidden;
  }

  toggle() {
    if (this.page) this.tellOverlay("gripi:scratch-hide");
    else if (this.shown()) this.hide();
    else this.show().catch(() => this.hide());
  }

  async show() {
    if (!this.overlay) return;
    this.returnFocus = this.document.activeElement;
    this.overlay.hidden = false;
    this.document.body.classList.remove("scratch-done");
    this.setPageInert(true);
    const drawer = this.document.getElementById("mobile-session-toggle");
    if (drawer) drawer.checked = false;
    await this.load();
  }

  // The gateway answers with the scratch session there is, or starts one, such as after the last one's Pi ended.
  async load() {
    // The overlay says that the session starts, unless it shows one already.
    if ((this.frame.getAttribute("src") || "about:blank") === "about:blank") this.overlay.classList.add("is-loading");
    const response = await this.window.fetch("/scratch/new", { method: "POST", headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Scratch session failed with HTTP ${response.status}`);
    const { redirect } = await response.json();
    this.markExists(true);
    if (this.frame.getAttribute("src") === redirect) {
      this.focusFrame();
      return;
    }
    this.overlay.classList.add("is-loading");
    this.clearActivity();
    this.frame.src = redirect;
  }

  // Hiding leaves the session as it is, and the cursor where it was before the overlay.
  hide() {
    if (!this.shown()) return;
    this.overlay.hidden = true;
    this.setPageInert(false);
    if (this.returnFocus?.isConnected && this.returnFocus !== this.document.body) this.returnFocus.focus({ preventScroll: true });
    else this.focusConversation();
    this.returnFocus = null;
  }

  // The frame leaves the session before it ends, so that its page doesn't say that it has ended.
  async startOver() {
    this.frame.src = "about:blank";
    this.clearActivity();
    this.overlay.classList.add("is-loading");
    await this.window.fetch("/scratch/discard", { method: "POST", headers: { Accept: "application/json" } });
    this.markExists(false);
    await this.load();
  }

  discarded() {
    this.frame.src = "about:blank";
    this.markExists(false);
    this.hide();
  }

  markExists(exists) {
    for (const button of this.document.querySelectorAll("[data-scratch-toggle]")) button.classList.toggle("is-active", exists);
    if (!exists) this.clearActivity();
  }

  // The button shows that Pi works, and then that it finished while the overlay was hidden, until the overlay shows.
  // The body keeps this, as the sidebar, with its button, is rendered again as it refreshes.
  markWorking(working) {
    const body = this.document.body;
    if (!working && body.classList.contains("scratch-working") && !this.shown()) body.classList.add("scratch-done");
    body.classList.toggle("scratch-working", working);
  }

  clearActivity() {
    this.document.body.classList.remove("scratch-working", "scratch-done");
  }

  setPageInert(inert) {
    this.document.body.classList.toggle("scratch-overlay-open", inert);
    for (const element of this.document.querySelectorAll(".app-shell")) element.inert = inert;
  }

  focusFrame() {
    this.frame.contentWindow?.postMessage("gripi:scratch-focus", this.window.location.origin);
  }

  initializePage() {
    this.document.addEventListener("click", (event) => {
      if (event.target.closest("[data-scratch-hide]")) this.tellOverlay("gripi:scratch-hide");
      if (event.target.closest("[data-scratch-discard]")) this.discard().catch(() => {});
      if (event.target.closest("[data-scratch-restart]")) this.restart().catch(() => {});
    });
    this.window.addEventListener("message", (event) => {
      if (event.origin !== this.window.location.origin || event.source !== this.window.parent) return;
      if (event.data === "gripi:scratch-focus") this.focusConversation();
    });
  }

  async discard() {
    const response = await this.window.fetch("/scratch/discard", { method: "POST", headers: { Accept: "application/json" } });
    if (response.ok) this.tellOverlay("gripi:scratch-discarded");
  }

  async restart() {
    const response = await this.window.fetch("/scratch/new", { method: "POST", headers: { Accept: "application/json" } });
    if (!response.ok) return;
    const { redirect } = await response.json();
    this.window.location.replace(redirect);
  }

  // While the overlay shows, the page around it keeps its palette and session search closed, and its page has none.
  blocksPageShortcuts() {
    return this.page || this.shown();
  }

  // Ctrl+N starts the scratch session over while its overlay shows, but not while the overlay loads one.
  requestNewSession() {
    if (this.page) this.tellOverlay("gripi:scratch-new-session");
    else if (!this.shown()) return false;
    else if (!this.overlay.classList.contains("is-loading")) this.startOver().catch(() => this.hide());
    return true;
  }

  // A desktop app's menu event, for the scratch session while its overlay shows.
  passToFrame(event) {
    if (!this.shown()) return false;
    const frameWindow = this.frame.contentWindow;
    frameWindow?.dispatchEvent(new frameWindow.CustomEvent(event.type, { detail: event.detail }));
    return true;
  }

  // The page tells the overlay when Pi starts and stops working.
  reportWorking(working) {
    if (!this.page || working === this.working) return;
    this.working = working;
    this.tellOverlay(working ? "gripi:scratch-working" : "gripi:scratch-idle");
  }

  // The session ended, such as with its Pi or in another tab.
  gone() {
    if (!this.page) return;
    this.document.body.classList.add("scratch-gone");
    this.tellOverlay("gripi:scratch-gone");
    const notice = this.document.querySelector("[data-scratch-gone]");
    if (notice) notice.hidden = false;
  }

  tellOverlay(message) {
    if (this.window.parent !== this.window) this.window.parent.postMessage(message, this.window.location.origin);
  }
}

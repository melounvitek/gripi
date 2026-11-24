// The scratch overlay shows the page of the user's scratch session, a Pi session that Gripi keeps nothing of, in a
// frame over the page, which stays as it is. On that page, the controller passes on to the overlay what the page's
// buttons and keys ask for.
export class ScratchController {
  constructor(documentObject, windowObject, toggleCommandPalette, openNewSessionModal, focusConversation) {
    this.document = documentObject;
    this.window = windowObject;
    this.toggleCommandPalette = toggleCommandPalette;
    this.openNewSessionModal = openNewSessionModal;
    this.focusConversation = focusConversation;
    this.page = documentObject.body.classList.contains("scratch-page");
    this.overlay = null;
    this.frame = null;
    this.returnFocus = null;
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
      if (event.data === "gripi:scratch-command-palette") {
        this.hide();
        this.toggleCommandPalette();
      }
      if (event.data === "gripi:scratch-new-session") {
        this.hide();
        this.openNewSessionModal();
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
    this.setPageInert(true);
    const drawer = this.document.getElementById("mobile-session-toggle");
    if (drawer) drawer.checked = false;
    // The gateway answers with the scratch session there is, or starts one, such as after the last one's Pi ended.
    const response = await this.window.fetch("/scratch/new", { method: "POST", headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(`Scratch session failed with HTTP ${response.status}`);
    const { redirect } = await response.json();
    this.markExists(true);
    if (this.frame.getAttribute("src") === redirect) {
      this.focusFrame();
      return;
    }
    this.overlay.classList.add("is-loading");
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

  discarded() {
    this.frame.src = "about:blank";
    this.markExists(false);
    this.hide();
  }

  markExists(exists) {
    for (const button of this.document.querySelectorAll("[data-scratch-toggle]")) button.classList.toggle("is-active", exists);
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

  // Esc hides the overlay, once nothing on the page takes it, and never stops Pi.
  hideFromEscape() {
    if (this.page) this.tellOverlay("gripi:scratch-hide");
    else if (this.shown()) this.hide();
    else return false;
    return true;
  }

  // The scratch page has no palette or sessions, so Ctrl+K and Ctrl+N there reach the page around it.
  requestCommandPalette() {
    if (!this.page) return false;
    this.tellOverlay("gripi:scratch-command-palette");
    return true;
  }

  requestNewSession() {
    if (!this.page) return false;
    this.tellOverlay("gripi:scratch-new-session");
    return true;
  }

  // Pi ended, such as after a crash, and the session with it.
  gone() {
    if (!this.page) return;
    this.document.body.classList.add("scratch-gone");
    const notice = this.document.querySelector("[data-scratch-gone]");
    if (notice) notice.hidden = false;
  }

  tellOverlay(message) {
    if (this.window.parent !== this.window) this.window.parent.postMessage(message, this.window.location.origin);
  }
}

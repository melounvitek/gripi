import { TOOL_OUTPUT_DESKTOP_TAIL_LINES } from "./constants.js";
import { activateToolOutputRegion, deactivateToolOutputRegion } from "./dom.js";

export class CurrentSessionFindController {
  constructor(document, conversation) {
    this.document = document;
    this.conversation = conversation;
    this.bindingEpoch = 0;
    this.matches = [];
    this.index = -1;
    this.highlightedMatch = null;
    this.observer = null;
    this.refreshFrame = null;
    this.preparationPromise = null;
    this.ownsHistoryLoad = false;
    this.preparationEpoch = 0;
    this.historyStatus = "complete";
    this.expandedToolOutput = null;
  }

  bind() {
    this.close({ restoreFocus: false });
    this.bindingEpoch += 1;
    this.bar = this.document.querySelector("[data-current-session-find]");
    this.input = this.bar?.querySelector("[data-current-session-find-input]") || null;
    this.count = this.bar?.querySelector("[data-current-session-find-count]") || null;
    this.conversationOnly = this.bar?.querySelector("[data-current-session-find-conversation-only]") || null;
    this.historyStatus = "pending";
    this.input?.addEventListener("input", () => {
      this.search({ resetIndex: true }).catch(() => {});
    });
    this.input?.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      this.move(event.shiftKey ? -1 : 1);
    });
    this.conversationOnly?.addEventListener("change", () => {
      this.search({ resetIndex: true }).catch(() => {});
    });
    this.bar?.querySelector("[data-current-session-find-previous]")?.addEventListener("click", () => this.move(-1));
    this.bar?.querySelector("[data-current-session-find-next]")?.addEventListener("click", () => this.move(1));
    this.bar?.querySelector("[data-current-session-find-close]")?.addEventListener("click", () => this.close());
  }

  get available() {
    return !!this.bar && !!this.input;
  }

  get open() {
    return !!this.bar && !this.bar.hidden;
  }

  ranges(text, query) {
    if (!query) return [];
    const ranges = [];
    const pattern = new RegExp(query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "giu");
    for (const match of text.matchAll(pattern)) ranges.push({ start: match.index, end: match.index + match[0].length });
    return ranges;
  }

  conversationMessage(message) {
    return message.matches('[data-role="user"].message--user:not(.message--compact):not(.message--thinking):not(.message--error):not(.message--tool-error), [data-role="assistant"].message--assistant:not(.message--compact):not(.message--thinking):not(.message--status):not(.message--tool):not(.message--tool-call):not(.message--tool-transcript):not(.message--error):not(.message--tool-error)');
  }

  textNodes(root) {
    if (!root) return [];
    const nodes = [];
    const nodeFilter = this.document.defaultView?.NodeFilter || globalThis.NodeFilter;
    const walker = this.document.createTreeWalker(root, nodeFilter.SHOW_TEXT, {
      acceptNode(node) {
        if (!node.nodeValue) return nodeFilter.FILTER_REJECT;
        if (node.parentElement?.closest("button, input, select, textarea, [contenteditable]")) return nodeFilter.FILTER_REJECT;
        return nodeFilter.FILTER_ACCEPT;
      }
    });
    while (walker.nextNode()) nodes.push(walker.currentNode);
    return nodes;
  }

  rootText(root) {
    return this.textNodes(root).map((node) => node.nodeValue).join("");
  }

  source(root) {
    const body = root.matches?.("[data-tool-output-body]") ? root : null;
    const collapse = body?.closest("[data-tool-output-collapse]") || null;
    if (!body || collapse?.dataset.collapsed !== "true") return { root, collapse, text: this.rootText(root) };
    const template = collapse.querySelector("[data-tool-output-full]");
    return { root: body, collapse, text: this.rootText(template?.content) || body.dataset.rawText || this.rootText(body) };
  }

  collectMatches() {
    const element = this.conversation.element;
    const query = this.input?.value;
    if (!element || !query) return [];
    const matches = [];
    element.querySelectorAll(".message").forEach((message) => {
      if (this.conversationOnly?.checked && !this.conversationMessage(message)) return;
      message.querySelectorAll(".compact-summary, .message-body").forEach((root) => {
        const source = this.source(root);
        this.ranges(source.text, query).forEach((range) => matches.push({ ...source, ...range, elements: [] }));
      });
    });
    return matches;
  }

  removeHighlights() {
    this.highlightedMatch?.elements.forEach((mark) => {
      const parent = mark.parentNode;
      mark.replaceWith(...mark.childNodes);
      parent?.normalize();
    });
    if (this.highlightedMatch) this.highlightedMatch.elements = [];
    this.highlightedMatch = null;
  }

  restoreToolOutput(exceptCollapse = null) {
    const expanded = this.expandedToolOutput;
    if (!expanded || expanded.collapse === exceptCollapse) return;
    this.expandedToolOutput = null;
    const { collapse, body, tailTemplate, control, button } = expanded;
    if (!collapse.isConnected || !body.isConnected) return;
    deactivateToolOutputRegion(body);
    if (body.dataset.rawText !== undefined) {
      const lines = String(body.dataset.rawText).split("\n");
      if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
      if (lines.length <= TOOL_OUTPUT_DESKTOP_TAIL_LINES) {
        delete collapse.dataset.expanded;
        collapse.dataset.collapsed = "false";
        if (button) button.setAttribute("aria-expanded", expanded.originalAriaExpanded);
        return;
      }
    }
    if (expanded.originalExpanded === undefined) delete collapse.dataset.expanded;
    else collapse.dataset.expanded = expanded.originalExpanded;
    collapse.dataset.collapsed = expanded.originalCollapsed;
    if (button) button.setAttribute("aria-expanded", expanded.originalAriaExpanded);
    if (control) control.hidden = expanded.originalControlHidden;
    if (tailTemplate?.content.childNodes.length) body.replaceChildren(...Array.from(tailTemplate.content.cloneNode(true).childNodes));
    else body.replaceChildren(...expanded.originalBodyNodes.map((node) => node.cloneNode(true)));
  }

  revealToolOutput(match) {
    const collapse = match?.collapse;
    if (!collapse || collapse.dataset.collapsed !== "true") return false;
    const body = collapse.querySelector("[data-tool-output-body]");
    const template = collapse.querySelector("[data-tool-output-full]");
    const tailTemplate = collapse.querySelector("[data-tool-output-tail]");
    const control = collapse.querySelector("[data-tool-output-collapse-control]");
    const button = collapse.querySelector("[data-tool-output-toggle]");
    if (!body) return false;
    activateToolOutputRegion(body);
    this.expandedToolOutput = {
      collapse, body, tailTemplate, control, button,
      originalExpanded: collapse.dataset.expanded,
      originalCollapsed: collapse.dataset.collapsed,
      originalControlHidden: control?.hidden || false,
      originalAriaExpanded: button?.getAttribute("aria-expanded") || "false",
      originalBodyNodes: Array.from(body.childNodes).map((node) => node.cloneNode(true))
    };
    collapse.dataset.expanded = "true";
    collapse.dataset.collapsed = "false";
    if (button) button.setAttribute("aria-expanded", "true");
    if (control) control.hidden = true;
    if (template?.content.childNodes.length) body.replaceChildren(...Array.from(template.content.cloneNode(true).childNodes));
    else if (body.dataset.rawText) body.textContent = body.dataset.rawText;
    return true;
  }

  highlight(match) {
    const nodes = this.textNodes(match.root);
    const portions = [];
    let offset = 0;
    nodes.forEach((node) => {
      const nodeEnd = offset + node.nodeValue.length;
      const start = Math.max(match.start, offset);
      const end = Math.min(match.end, nodeEnd);
      if (start < end) portions.push({ node, start: start - offset, end: end - offset });
      offset = nodeEnd;
    });
    portions.reverse().forEach((portion) => {
      const selected = portion.start === 0 ? portion.node : portion.node.splitText(portion.start);
      const length = portion.end - portion.start;
      if (length < selected.nodeValue.length) selected.splitText(length);
      const mark = this.document.createElement("mark");
      mark.className = "current-session-find-match is-active";
      selected.replaceWith(mark);
      mark.append(selected);
      match.elements.unshift(mark);
    });
  }

  observe() {
    if (!this.open || !this.conversation.element) return;
    if (!this.observer) {
      const MutationObserver = this.document.defaultView?.MutationObserver || globalThis.MutationObserver;
      this.observer = new MutationObserver(() => this.scheduleRefresh());
    }
    this.observer.observe(this.conversation.element, { childList: true, subtree: true, characterData: true });
  }

  renderHighlights() {
    this.observer?.disconnect();
    this.removeHighlights();
    const activeMatch = this.matches[this.index];
    this.restoreToolOutput(activeMatch?.collapse);
    this.revealToolOutput(activeMatch);
    if (activeMatch && activeMatch.collapse?.dataset.collapsed !== "true") {
      this.highlight(activeMatch);
      this.highlightedMatch = activeMatch;
    }
    this.observer?.takeRecords();
    this.observe();
  }

  updateCount() {
    if (!this.count) return;
    const current = this.index >= 0 ? this.index + 1 : 0;
    this.count.textContent = `${current} / ${this.matches.length}`;
  }

  scrollMatchIntoView() {
    const element = this.matches[this.index]?.elements[0];
    const scroll = this.conversation.element;
    if (!scroll || !element) return;

    const toolOutput = element.closest?.("[data-tool-output-body]");
    if (toolOutput && toolOutput.scrollHeight > toolOutput.clientHeight) {
      const outputRect = toolOutput.getBoundingClientRect();
      const elementRect = element.getBoundingClientRect();
      const top = toolOutput.scrollTop + elementRect.top - outputRect.top - ((toolOutput.clientHeight - elementRect.height) / 2);
      const maximumTop = toolOutput.scrollHeight - toolOutput.clientHeight;
      toolOutput.scrollTo({ top: Math.min(Math.max(top, 0), maximumTop), behavior: "auto" });
    }

    const scrollRect = scroll.getBoundingClientRect();
    const elementRect = element.getBoundingClientRect();
    const top = scroll.scrollTop + elementRect.top - scrollRect.top - ((scroll.clientHeight - elementRect.height) / 2);
    this.conversation.stopAutoFollow();
    this.conversation.withProgrammaticScroll(() => scroll.scrollTo({ top: Math.max(top, 0), behavior: "smooth" }));
  }

  refresh({ resetIndex = false } = {}) {
    if (!this.open || this.historyStatus !== "complete") return;
    this.observer?.disconnect();
    this.removeHighlights();
    const previousIndex = resetIndex ? 0 : this.index;
    this.matches = this.collectMatches();
    this.index = this.matches.length > 0 ? Math.min(Math.max(previousIndex, 0), this.matches.length - 1) : -1;
    this.renderHighlights();
    this.updateCount();
    this.scrollMatchIntoView();
  }

  scheduleRefresh() {
    if (!this.open || this.historyStatus !== "complete" || this.preparationPromise || this.refreshFrame) return;
    const epoch = this.bindingEpoch;
    const frame = requestAnimationFrame(() => {
      if (this.refreshFrame !== frame) return;
      this.refreshFrame = null;
      if (epoch === this.bindingEpoch) this.refresh();
    });
    this.refreshFrame = frame;
  }

  move(direction) {
    if (this.matches.length === 0) return;
    this.index = (this.index + direction + this.matches.length) % this.matches.length;
    this.renderHighlights();
    this.updateCount();
    this.scrollMatchIntoView();
  }

  queryReady() {
    return Array.from(this.input?.value || "").length >= 3;
  }

  clearMatches(message) {
    this.observer?.disconnect();
    this.removeHighlights();
    this.restoreToolOutput();
    this.matches = [];
    this.index = -1;
    if (this.count && message) this.count.textContent = message;
    else this.updateCount();
  }

  async search({ resetIndex = false } = {}) {
    if (!this.open) return;
    if (!this.queryReady()) {
      if (this.preparationPromise && this.ownsHistoryLoad) this.conversation.cancelOlderHistory?.();
      this.preparationPromise = null;
      this.ownsHistoryLoad = false;
      this.preparationEpoch += 1;
      if (this.historyStatus !== "complete") this.historyStatus = "pending";
      this.clearMatches("Type 3+ characters");
      return;
    }
    if (this.historyStatus === "complete") {
      this.refresh({ resetIndex });
      return;
    }
    if (this.preparationPromise) return this.preparationPromise;

    this.historyStatus = "loading";
    if (this.count) this.count.textContent = "Loading…";
    const epoch = this.bindingEpoch;
    const preparationEpoch = ++this.preparationEpoch;
    const bar = this.bar;
    const scroll = this.conversation.element;
    const conversationEpoch = this.conversation.bindingEpoch;
    this.ownsHistoryLoad = !this.conversation.olderHistoryLoading;
    const preparation = (async () => {
      const historyStatus = await this.conversation.loadOlderHistory();
      if (!this.open || epoch !== this.bindingEpoch || preparationEpoch !== this.preparationEpoch || conversationEpoch !== this.conversation.bindingEpoch || bar !== this.bar || scroll !== this.conversation.element) return;
      this.historyStatus = historyStatus;
      if (historyStatus === "failed" || historyStatus === "cancelled") {
        this.historyStatus = "pending";
        this.clearMatches("History incomplete");
        return;
      }
      if (historyStatus === "complete" && this.queryReady()) this.refresh({ resetIndex });
    })();
    this.preparationPromise = preparation;
    try {
      await preparation;
    } finally {
      if (this.preparationPromise === preparation) {
        this.preparationPromise = null;
        this.ownsHistoryLoad = false;
      }
    }
  }

  async show() {
    if (!this.available) return;
    this.bar.hidden = false;
    this.input.focus({ preventScroll: true });
    this.input.select();
    return this.search({ resetIndex: true });
  }

  close({ restoreFocus = true } = {}) {
    this.observer?.disconnect();
    if (this.preparationPromise && this.ownsHistoryLoad) this.conversation.cancelOlderHistory?.();
    this.ownsHistoryLoad = false;
    if (this.refreshFrame) cancelAnimationFrame(this.refreshFrame);
    this.refreshFrame = null;
    this.preparationPromise = null;
    this.preparationEpoch += 1;
    if (this.historyStatus !== "complete") this.historyStatus = "pending";
    this.removeHighlights();
    this.restoreToolOutput();
    this.matches = [];
    this.index = -1;
    this.updateCount();
    if (this.bar) this.bar.hidden = true;
    if (restoreFocus) this.conversation.element?.focus({ preventScroll: true });
  }
}

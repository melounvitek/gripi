export const TREE_FILTERS = [
  { value: "default", label: "Default" },
  { value: "no-tools", label: "No tools" },
  { value: "user-only", label: "User only" },
  { value: "labeled-only", label: "Labeled only" },
  { value: "all", label: "All entries" }
];

export const TREE_SUMMARY_CHOICES = [
  { value: "none", label: "No summary" },
  { value: "default", label: "Summarize" },
  { value: "custom", label: "Summarize with custom instructions" }
];

function entrySearchText(entry) {
  return [entry.role, entry.type, entry.text, entry.label, entry.timestamp, entry.labelTimestamp]
    .filter(Boolean)
    .join(" ")
    .toLocaleLowerCase();
}

export class TreeSessionModel {
  constructor(entries = []) {
    this.entries = entries;
    this.byId = new Map(entries.map((entry) => [entry.entryId, entry]));
    this.children = new Map();
    this.roots = [];
    this.collapsed = new Set();
    this.query = "";
    entries.forEach((entry) => {
      if (entry.parentId && this.byId.has(entry.parentId)) {
        const children = this.children.get(entry.parentId) || [];
        children.push(entry);
        this.children.set(entry.parentId, children);
      } else {
        this.roots.push(entry);
      }
    });
    this.selectedId = entries.find((entry) => entry.current)?.entryId || entries[0]?.entryId || null;
  }

  select(entryId) {
    if (this.byId.has(entryId)) this.selectedId = entryId;
  }

  setSearch(query) {
    this.query = String(query || "").trim().toLocaleLowerCase();
    const visibleIds = new Set(this.visibleEntries().map((entry) => entry.entryId));
    if (!visibleIds.has(this.selectedId)) this.selectedId = this.visibleEntries()[0]?.entryId || null;
  }

  hasChildren(entryId) {
    return (this.children.get(entryId) || []).length > 0;
  }

  matchingIds() {
    if (!this.query) return null;
    const tokens = this.query.split(/\s+/).filter(Boolean);
    return new Set(this.entries
      .filter((entry) => tokens.every((token) => entrySearchText(entry).includes(token)))
      .map((entry) => entry.entryId));
  }

  visibleEntries() {
    const matchingIds = this.matchingIds();
    return this.entries.filter((entry) => {
      if (matchingIds && !matchingIds.has(entry.entryId)) return false;
      let parent = this.byId.get(entry.parentId);
      while (parent) {
        if (this.collapsed.has(parent.entryId)) return false;
        parent = this.byId.get(parent.parentId);
      }
      return true;
    });
  }

  visibleStructure() {
    const entries = this.visibleEntries();
    const visibleIds = new Set(entries.map((entry) => entry.entryId));
    const children = new Map();
    const roots = [];
    entries.forEach((entry) => {
      let parent = this.byId.get(entry.parentId);
      while (parent && !visibleIds.has(parent.entryId)) parent = this.byId.get(parent.parentId);
      if (!parent) {
        roots.push(entry);
        return;
      }
      const siblings = children.get(parent.entryId) || [];
      siblings.push(entry);
      children.set(parent.entryId, siblings);
    });
    const visual = new Map();
    // Pi keeps linear chains flat and adds a visual level only at a fork and its first generation; multiple roots share a virtual fork.
    const multipleRoots = roots.length > 1;
    const stack = [];
    for (let index = roots.length - 1; index >= 0; index -= 1) {
      stack.push({
        entry: roots[index], indent: multipleRoots ? 1 : 0,
        justBranched: multipleRoots, showConnector: multipleRoots,
        isLast: index === roots.length - 1, gutters: [], virtualRootChild: multipleRoots
      });
    }
    while (stack.length) {
      const current = stack.pop();
      const displayIndent = multipleRoots ? Math.max(0, current.indent - 1) : current.indent;
      visual.set(current.entry.entryId, {
        indent: displayIndent,
        showConnector: current.showConnector && !current.virtualRootChild,
        isLast: current.isLast,
        gutters: current.gutters
      });
      const visibleChildren = children.get(current.entry.entryId) || [];
      const branched = visibleChildren.length > 1;
      const childIndent = branched || (current.justBranched && current.indent > 0) ? current.indent + 1 : current.indent;
      const connectorPosition = Math.max(0, displayIndent - 1);
      const childGutters = current.showConnector && !current.virtualRootChild
        ? [...current.gutters, { position: connectorPosition, show: !current.isLast }]
        : current.gutters;
      for (let index = visibleChildren.length - 1; index >= 0; index -= 1) {
        stack.push({
          entry: visibleChildren[index], indent: childIndent,
          justBranched: branched, showConnector: branched,
          isLast: index === visibleChildren.length - 1, gutters: childGutters,
          virtualRootChild: false
        });
      }
    }
    return { entries, children, roots, visual };
  }

  move(direction) {
    const structure = this.visibleStructure();
    const visible = structure.entries;
    if (visible.length === 0) return null;
    let index = Math.max(0, visible.findIndex((entry) => entry.entryId === this.selectedId));
    const selected = visible[index];
    if (direction === "left") {
      if (this.hasChildren(selected.entryId) && !this.collapsed.has(selected.entryId)) {
        this.collapsed.add(selected.entryId);
      } else {
        const visibleIds = new Set(visible.map((entry) => entry.entryId));
        let parent = this.byId.get(selected.parentId);
        while (parent && !visibleIds.has(parent.entryId)) parent = this.byId.get(parent.parentId);
        if (parent) this.selectedId = parent.entryId;
      }
    } else if (direction === "right") {
      const children = structure.children.get(selected.entryId) || [];
      if (this.hasChildren(selected.entryId) && this.collapsed.has(selected.entryId)) {
        this.collapsed.delete(selected.entryId);
      } else if (children.length) {
        this.selectedId = children[0].entryId;
      }
    } else {
      const offsets = { up: -1, down: 1, pageUp: -10, pageDown: 10 };
      if (direction === "home") index = 0;
      else if (direction === "end") index = visible.length - 1;
      else index = Math.max(0, Math.min(visible.length - 1, index + (offsets[direction] || 0)));
      this.selectedId = visible[index].entryId;
    }
    return this.selectedId;
  }
}

function displayTimestamp(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

export class TreeSessionController {
  constructor(document, window, callbacks = {}) {
    this.document = document;
    this.window = window;
    this.callbacks = callbacks;
    this.model = null;
    this.settings = {};
    this.loading = false;
    this.navigating = false;
    this.savingLabel = false;
    this.filterChosen = false;
    this.treeUrl = null;
    this.operation = 0;
    this.bind();
  }

  modal() {
    return this.document.querySelector('[data-modal="tree-session-modal"]');
  }

  open() {
    const modal = this.modal();
    if (!modal) return false;
    this.filterChosen = false;
    const options = modal.querySelector("[data-tree-options]");
    if (options) options.open = false;
    const search = modal.querySelector("[data-tree-search]");
    if (search) search.value = "";
    const labelTimestamps = modal.querySelector("[data-tree-label-timestamps]");
    if (labelTimestamps) labelTimestamps.checked = false;
    if (this.model) {
      this.model.setSearch("");
      this.render();
    }
    this.showTreeStep(modal);
    this.callbacks.openModal?.(modal);
    this.load(modal).catch(() => {});
    return true;
  }

  close() {
    this.operation += 1;
    this.loading = false;
    this.callbacks.closeModal?.(this.modal());
  }

  bind() {
    this.document.addEventListener("click", (event) => this.handleClick(event));
    this.document.addEventListener("input", (event) => this.handleInput(event));
    this.document.addEventListener("change", (event) => this.handleChange(event));
    this.document.addEventListener("submit", (event) => this.handleSubmit(event));
    this.document.addEventListener("keydown", (event) => this.handleKeydown(event));
  }

  async load(modal = this.modal()) {
    const list = modal?.querySelector("[data-tree-session-list]");
    const baseUrl = list?.dataset.treeEntriesUrl;
    if (!list || !baseUrl) return;
    const operation = ++this.operation;
    this.loading = true;
    list.setAttribute("aria-busy", "true");
    this.setStatus("Loading session tree…");
    try {
      const url = new URL(baseUrl, this.window.location.origin);
      const filter = modal.querySelector("[data-tree-filter]")?.value;
      if (filter && this.filterChosen) url.searchParams.set("filter", filter);
      const response = await fetch(url, { headers: { "Accept": "application/json" } });
      const payload = await response.json().catch(() => null);
      if (!response.ok || !payload) throw new Error(payload?.error || "Could not load session tree.");
      if (operation !== this.operation || modal !== this.modal() || modal.hidden) return;
      this.settings = payload.settings || {};
      const sameTree = this.treeUrl === baseUrl;
      const previousSelectedId = sameTree ? this.model?.selectedId : null;
      const collapsed = sameTree ? this.model?.collapsed || new Set() : new Set();
      this.model = new TreeSessionModel(Array.isArray(payload.entries) ? payload.entries : []);
      this.treeUrl = baseUrl;
      if (previousSelectedId) this.model.select(previousSelectedId);
      this.model.collapsed = new Set([...collapsed].filter((entryId) => this.model.byId.has(entryId)));
      const effectiveFilter = payload.filter || filter;
      const filterControl = modal.querySelector("[data-tree-filter]");
      if (filterControl && effectiveFilter) filterControl.value = effectiveFilter;
      this.model.setSearch(modal.querySelector("[data-tree-search]")?.value || "");
      this.render();
      const count = this.model.entries.length;
      this.setStatus(count ? `${count} ${count === 1 ? "entry" : "entries"}${payload.truncated ? ` shown of ${payload.totalEntries}` : ""}.` : "No session tree entries are available.");
    } catch (error) {
      if (operation === this.operation) this.setStatus(error.message || "Could not load session tree.", true);
    } finally {
      if (operation === this.operation) {
        this.loading = false;
        list.setAttribute("aria-busy", "false");
      }
    }
  }

  setStatus(message, error = false) {
    const status = this.modal()?.querySelector("[data-tree-session-status]");
    if (!status) return;
    status.textContent = message;
    status.classList.toggle("is-error", error);
  }

  render({ focus = false } = {}) {
    const modal = this.modal();
    const viewport = modal?.querySelector("[data-tree-viewport]");
    if (!viewport || !this.model) return;
    viewport.replaceChildren();
    const structure = this.model.visibleStructure();
    const appendEntries = (entries, parent) => {
      entries.forEach((entry) => {
        const item = this.document.createElement("li");
        item.className = "tree-session-node";
        item.dataset.treeFocusEntry = entry.entryId;
        item.id = `tree-entry-${encodeURIComponent(entry.entryId).replace(/%/g, "-")}`;
        item.setAttribute("role", "treeitem");
        item.setAttribute("aria-selected", String(entry.entryId === this.model.selectedId));
        item.tabIndex = entry.entryId === this.model.selectedId ? 0 : -1;
        const row = this.document.createElement("div");
        row.className = "tree-session-row";
        row.classList.toggle("is-active", entry.entryId === this.model.selectedId);
        row.classList.toggle("is-current", !!entry.current);

        const visual = structure.visual.get(entry.entryId);
        const leading = this.document.createElement("span");
        leading.className = "tree-session-connectors";
        leading.setAttribute("aria-hidden", "true");
        for (let level = 0; level < visual.indent; level += 1) {
          const connector = this.document.createElement("span");
          connector.className = "tree-session-connector-level";
          const gutter = visual.gutters.find((candidate) => candidate.position === level);
          if (gutter?.show) connector.textContent = "│";
          if (visual.showConnector && level === visual.indent - 1) connector.textContent = visual.isLast ? "└" : "├";
          leading.append(connector);
        }
        row.append(leading);

        const allChildren = this.model.children.get(entry.entryId) || [];
        const children = structure.children.get(entry.entryId) || [];
        if (allChildren.length) {
          const fold = this.document.createElement("button");
          const collapsed = this.model.collapsed.has(entry.entryId);
          fold.type = "button";
          fold.className = "tree-session-fold secondary";
          fold.dataset.treeFold = entry.entryId;
          fold.tabIndex = -1;
          fold.setAttribute("aria-label", `${collapsed ? "Expand" : "Collapse"} branch`);
          fold.textContent = collapsed ? "▸" : "▾";
          item.setAttribute("aria-expanded", String(!collapsed));
          row.append(fold);
        } else {
          const spacer = this.document.createElement("span");
          spacer.className = "tree-session-fold-spacer";
          row.append(spacer);
        }

        const button = this.document.createElement("div");
        button.className = "tree-session-entry";
        button.dataset.treeEntryId = entry.entryId;

        const heading = this.document.createElement("span");
        heading.className = "tree-session-entry-heading";
        const role = this.document.createElement("span");
        role.className = "tree-session-role";
        role.textContent = entry.role || entry.type || "entry";
        const text = this.document.createElement("span");
        text.className = "tree-session-text";
        text.textContent = entry.text || "Untitled entry";
        heading.append(role, text);
        if (entry.current) heading.append(this.badge("Current", "current"));
        if (entry.latest && !entry.current) heading.append(this.badge("Latest", "latest"));
        button.append(heading);

        const metadata = this.document.createElement("span");
        metadata.className = "tree-session-meta";
        const timestamp = displayTimestamp(entry.timestamp);
        const values = [];
        if (entry.label) values.push(`Label: ${entry.label}`);
        if (timestamp) values.push(timestamp);
        const showLabelTimestamps = modal.querySelector("[data-tree-label-timestamps]")?.checked;
        if (showLabelTimestamps && entry.labelTimestamp) values.push(`labeled ${displayTimestamp(entry.labelTimestamp)}`);
        metadata.textContent = values.join(" · ");
        if (values.length) button.append(metadata);
        row.append(button);
        item.append(row);

        if (children.length) {
          const group = this.document.createElement("ul");
          group.className = "tree-session-children";
          group.setAttribute("role", "group");
          appendEntries(children, group);
          item.append(group);
        }
        parent.append(item);
      });
    };
    appendEntries(structure.roots, viewport);
    this.syncSelectionControls();
    if (focus) [...modal.querySelectorAll("[data-tree-focus-entry]")]
      .find((entry) => entry.dataset.treeFocusEntry === this.model.selectedId)
      ?.focus({ preventScroll: true });
  }

  badge(label, kind) {
    const badge = this.document.createElement("span");
    badge.className = `tree-session-badge tree-session-badge--${kind}`;
    badge.textContent = label;
    return badge;
  }

  selectedEntry() {
    return this.model?.byId.get(this.model.selectedId) || null;
  }

  syncSelectionControls() {
    const modal = this.modal();
    const entry = this.selectedEntry();
    const input = modal?.querySelector("[data-tree-label-input]");
    if (input) input.value = entry?.label || "";
    [modal?.querySelector("[data-tree-label-save]"), modal?.querySelector("[data-tree-label-clear]")]
      .forEach((control) => { if (control) control.disabled = !entry || this.savingLabel; });
    const navigate = modal?.querySelector("[data-tree-navigate]");
    if (navigate) navigate.disabled = !entry || this.savingLabel;
  }

  select(entryId, focus = false) {
    this.model?.select(entryId);
    this.render({ focus });
  }

  toggleFold(entryId) {
    if (!this.model) return;
    if (this.model.collapsed.has(entryId)) this.model.collapsed.delete(entryId);
    else this.model.collapsed.add(entryId);
    this.render({ focus: true });
  }

  requestNavigation() {
    if (!this.selectedEntry()) return;
    if (this.settings?.branchSummary?.skipPrompt === true) {
      this.navigate("none", "");
    } else {
      const modal = this.modal();
      modal.querySelector("[data-tree-browser-step]").hidden = true;
      modal.querySelector("[data-tree-summary-step]").hidden = false;
      modal.querySelector('input[name="summary_mode"]:checked')?.focus();
    }
  }

  showTreeStep(modal = this.modal()) {
    if (!modal) return;
    const browser = modal.querySelector("[data-tree-browser-step]");
    const summary = modal.querySelector("[data-tree-summary-step]");
    if (browser) browser.hidden = false;
    if (summary) summary.hidden = true;
  }

  navigate(summaryMode, customInstructions) {
    const entry = this.selectedEntry();
    if (!entry) return;
    return this.navigateEntry(entry, summaryMode, customInstructions, { modal: this.modal() });
  }

  async navigateEntry(entry, summaryMode, customInstructions, { modal = null, button = null, errorRegion = null } = {}) {
    if (!entry?.entryId || this.navigating) return;
    const submit = modal?.querySelector("[data-tree-summary-submit]");
    const navigateButton = modal?.querySelector("[data-tree-navigate]");
    if (summaryMode === "custom" && !customInstructions.trim()) {
      this.setStatus("Custom summary instructions cannot be empty.", true);
      modal?.querySelector("[data-tree-custom-instructions]")?.focus();
      return;
    }
    if (errorRegion) {
      errorRegion.textContent = "";
      errorRegion.hidden = true;
    }
    const formData = new FormData();
    formData.set("session", this.callbacks.currentSessionPath?.() || "");
    formData.set("entry_id", entry.entryId);
    formData.set("summary_mode", summaryMode);
    if (summaryMode === "custom") formData.set("custom_instructions", customInstructions.trim());
    this.callbacks.addSessionViewFormParams?.(formData);
    this.navigating = true;
    if (submit) submit.disabled = true;
    if (navigateButton) navigateButton.disabled = true;
    if (button) button.disabled = true;
    this.callbacks.showSessionSwitching?.();
    try {
      const response = await fetch("/sessions/tree", { method: "POST", body: formData, headers: { "Accept": "application/json" } });
      const payload = await response.json().catch(() => null);
      if (!response.ok || !payload || payload.cancelled) throw new Error(payload?.error || "Could not navigate the session tree.");
      if (modal) this.callbacks.closeModal?.(modal);
      await this.callbacks.navigate?.(payload, entry);
    } catch (error) {
      const message = error.message || "Could not navigate the session tree.";
      if (errorRegion) {
        errorRegion.textContent = message;
        errorRegion.hidden = false;
      } else {
        this.showTreeStep(modal);
        this.setStatus(message, true);
      }
    } finally {
      this.navigating = false;
      if (submit) submit.disabled = false;
      if (navigateButton) navigateButton.disabled = !this.selectedEntry();
      if (button) button.disabled = false;
      this.callbacks.hideSessionSwitching?.();
    }
  }

  async saveLabel(label) {
    const entry = this.selectedEntry();
    if (!entry || this.savingLabel) return;
    this.savingLabel = true;
    this.syncSelectionControls();
    try {
      const formData = new FormData();
      formData.set("session", this.callbacks.currentSessionPath?.() || "");
      formData.set("entry_id", entry.entryId);
      formData.set("label", label);
      const response = await fetch("/sessions/tree/label", { method: "POST", body: formData, headers: { "Accept": "application/json" } });
      const payload = await response.json().catch(() => null);
      if (!response.ok || !payload) throw new Error(payload?.error || "Could not update the label.");
      entry.label = payload.label || null;
      entry.labelTimestamp = payload.label ? payload.labelTimestamp || null : null;
      await this.load();
      this.setStatus(payload.label ? "Label updated." : "Label cleared.");
    } finally {
      this.savingLabel = false;
      this.syncSelectionControls();
    }
  }

  handleClick(event) {
    const latest = event.target.closest?.("[data-tree-latest-entry-id]");
    if (latest) {
      event.preventDefault();
      const errorRegion = latest.closest(".tree-position-banner")?.querySelector("[data-tree-latest-error]");
      return this.navigateEntry({ entryId: latest.dataset.treeLatestEntryId }, "none", "", { button: latest, errorRegion });
    }
    const modal = event.target.closest?.('[data-modal="tree-session-modal"]');
    if (!modal) return;
    const fold = event.target.closest("[data-tree-fold]");
    if (fold) { event.preventDefault(); this.toggleFold(fold.dataset.treeFold); return; }
    const entry = event.target.closest("[data-tree-entry-id]");
    if (entry) { event.preventDefault(); this.select(entry.dataset.treeEntryId, true); return; }
    if (event.target.closest("[data-tree-navigate]")) { event.preventDefault(); this.requestNavigation(); return; }
    if (event.target.closest("[data-tree-summary-back]")) { event.preventDefault(); this.showTreeStep(modal); return; }
    if (event.target.closest("[data-tree-label-clear]")) {
      event.preventDefault();
      this.saveLabel("").catch((error) => this.setStatus(error.message, true));
    }
  }

  handleInput(event) {
    if (event.target.matches?.("[data-tree-search]")) {
      this.model?.setSearch(event.target.value);
      this.render();
    }
    if (event.target.matches?.("[data-tree-custom-instructions]")) this.syncSummaryChoice();
  }

  handleChange(event) {
    if (event.target.matches?.("[data-tree-filter]")) this.applyFilterChoice();
    if (event.target.matches?.("[data-tree-label-timestamps]")) this.render();
    if (event.target.matches?.('input[name="summary_mode"]')) this.syncSummaryChoice();
  }

  syncSummaryChoice() {
    const modal = this.modal();
    const custom = modal?.querySelector("[data-tree-custom-instructions]");
    const mode = modal?.querySelector('input[name="summary_mode"]:checked')?.value;
    if (custom) custom.hidden = mode !== "custom";
  }

  applyFilterChoice() {
    this.filterChosen = true;
    this.model = null;
    this.load().catch(() => {});
  }

  handleSubmit(event) {
    if (event.target.matches?.("[data-tree-label-form]")) {
      event.preventDefault();
      const input = event.target.querySelector("[data-tree-label-input]");
      this.saveLabel(input?.value || "").catch((error) => this.setStatus(error.message, true));
    }
    if (event.target.matches?.("[data-tree-summary-form]")) {
      event.preventDefault();
      const mode = event.target.querySelector('input[name="summary_mode"]:checked')?.value || "none";
      const instructions = event.target.querySelector("[data-tree-custom-instructions]")?.value || "";
      this.navigate(mode, instructions);
    }
  }

  revealOptions(modal = this.modal()) {
    const options = modal?.querySelector("[data-tree-options]");
    if (options) options.open = true;
  }

  handleKeydown(event) {
    const modal = this.document.querySelector('[data-modal="tree-session-modal"]:not([hidden])');
    if (!modal) return;
    const key = String(event.key || "");
    const searchShortcut = (key === "/" && !event.ctrlKey && !event.metaKey && !event.altKey && !event.shiftKey && !event.target.closest?.("input, textarea, select")) ||
      (key.toLowerCase() === "f" && (event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey);
    if (searchShortcut) {
      event.preventDefault();
      this.revealOptions(modal);
      modal.querySelector("[data-tree-search]")?.focus();
      return;
    }
    if (key.toLowerCase() === "o" && (event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey) {
      event.preventDefault();
      this.revealOptions(modal);
      const filter = modal.querySelector("[data-tree-filter]");
      if (filter) {
        const index = TREE_FILTERS.findIndex((choice) => choice.value === filter.value);
        filter.value = TREE_FILTERS[(index + 1) % TREE_FILTERS.length].value;
        this.applyFilterChoice();
      }
      return;
    }
    if (key.toLowerCase() === "l" && event.shiftKey && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      this.revealOptions(modal);
      modal.querySelector("[data-tree-label-input]")?.focus();
      return;
    }
    if (key === "Escape") {
      event.preventDefault();
      if (!modal.querySelector("[data-tree-summary-step]")?.hidden) this.showTreeStep(modal);
      else {
        const search = modal.querySelector("[data-tree-search]");
        if (search?.value) { search.value = ""; this.model?.setSearch(""); this.render(); }
        else this.close();
      }
      return;
    }
    if (event.target.closest?.("input, textarea, select")) return;
    if (!event.target.closest?.("[data-tree-focus-entry]")) return;
    const directions = { ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right", Home: "home", End: "end", PageUp: "pageUp", PageDown: "pageDown" };
    if (directions[key]) {
      event.preventDefault();
      this.model?.move(directions[key]);
      this.render({ focus: true });
    } else if (key === "Enter") {
      event.preventDefault();
      this.requestNavigation();
    }
  }
}

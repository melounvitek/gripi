const PATH_DELIMITERS = new Set([" ", "\t", "\n", '"', "'", "="]);

function lastDelimiterIndex(text) {
  for (let index = text.length - 1; index >= 0; index -= 1) {
    if (PATH_DELIMITERS.has(text[index])) return index;
  }
  return -1;
}

function unclosedQuoteStart(text) {
  let start = null;
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] === '"') start = start === null ? index : null;
  }
  return start;
}

function tokenBoundary(text, index) {
  return index === 0 || PATH_DELIMITERS.has(text[index - 1]);
}

export function composerPathContext(value, selectionStart, { force = false, natural = false } = {}) {
  const beforeCaret = value.slice(0, selectionStart);
  const lineStart = beforeCaret.lastIndexOf("\n") + 1;
  const currentLine = beforeCaret.slice(lineStart);
  const quoteStart = unclosedQuoteStart(currentLine);
  if (quoteStart !== null && tokenBoundary(currentLine, currentLine[quoteStart - 1] === "@" ? quoteStart - 1 : quoteStart)) {
    const localStart = currentLine[quoteStart - 1] === "@" ? quoteStart - 1 : quoteStart;
    const token = currentLine.slice(localStart);
    return {
      mode: token.startsWith("@") ? "fuzzy" : "path",
      query: token.slice(token.startsWith('@"') ? 2 : 1),
      token,
      start: lineStart + localStart,
      quoted: true
    };
  }

  const delimiter = lastDelimiterIndex(currentLine);
  const localStart = delimiter + 1;
  const token = currentLine.slice(localStart);
  if (token.startsWith("@") && tokenBoundary(currentLine, localStart)) {
    return { mode: "fuzzy", query: token.slice(1), token, start: lineStart + localStart, quoted: false };
  }
  if (!force && !(natural && (token.includes("/") || token.startsWith(".")))) return null;
  return { mode: "path", query: token, token, start: lineStart + localStart, quoted: false };
}

function completionValue(context, suggestion) {
  const path = suggestion.path;
  const quoted = context.quoted || path.includes(" ");
  return `${context.mode === "fuzzy" ? "@" : ""}${quoted ? `"${path}"` : path}`;
}

export function applyComposerPathCompletion(value, selectionStart, context, suggestion) {
  const replacement = completionValue(context, suggestion);
  let suffix = value.slice(selectionStart);
  if (context.quoted && replacement.endsWith('"') && suffix.startsWith('"')) suffix = suffix.slice(1);

  const trailingSpace = context.mode === "fuzzy" && !suggestion.directory ? " " : "";
  const nextValue = value.slice(0, context.start) + replacement + trailingSpace + suffix;
  const replacementEnd = context.start + replacement.length;
  const caret = suggestion.directory && replacement.endsWith('"') ? replacementEnd - 1 : replacementEnd + trailingSpace.length;
  return { value: nextValue, selectionStart: caret };
}

export class ComposerAutocompleteController {
  constructor(document, { currentSessionPath, debounceMs = 120 } = {}) {
    this.document = document;
    this.currentSessionPath = currentSessionPath || (() => "");
    this.debounceMs = debounceMs;
    this.textarea = null;
    this.list = null;
    this.suggestions = [];
    this.context = null;
    this.activeIndex = 0;
    this.composing = false;
    this.timer = null;
    this.request = null;
    this.requestVersion = 0;
    this.onInput = () => this.input();
    this.onCompositionStart = () => { this.composing = true; this.close(); };
    this.onCompositionEnd = () => { this.composing = false; this.input(); };
    this.onBlur = (event) => {
      if (!this.list?.contains(event.relatedTarget)) this.close();
    };
    this.onSelectionChange = () => {
      if (this.open && !this.currentContextMatches()) this.close();
    };
    this.onPointerDown = (event) => {
      const option = event.target.closest?.("[data-composer-path-option]");
      if (!option || !this.list?.contains(option)) return;
      event.preventDefault();
      if (event.pointerType !== "mouse") this.accept(Number(option.dataset.composerPathOption));
    };
    this.onClick = (event) => {
      const option = event.target.closest?.("[data-composer-path-option]");
      if (!option || !this.list?.contains(option)) return;
      this.accept(Number(option.dataset.composerPathOption));
    };
  }

  bind(textarea, list) {
    this.destroy();
    this.textarea = textarea || null;
    this.list = list || null;
    if (!this.textarea || !this.list) return;
    this.textarea.addEventListener("input", this.onInput);
    this.textarea.addEventListener("compositionstart", this.onCompositionStart);
    this.textarea.addEventListener("compositionend", this.onCompositionEnd);
    this.textarea.addEventListener("blur", this.onBlur);
    this.textarea.addEventListener("select", this.onSelectionChange);
    this.textarea.addEventListener("keyup", this.onSelectionChange);
    this.list.addEventListener("pointerdown", this.onPointerDown);
    this.list.addEventListener("click", this.onClick);
    this.list.hidden = true;
    this.list.replaceChildren();
    this.textarea.setAttribute("role", "combobox");
    this.textarea.setAttribute("aria-autocomplete", "list");
    this.textarea.setAttribute("aria-controls", this.list.id);
    this.textarea.setAttribute("aria-expanded", "false");
  }

  destroy() {
    clearTimeout(this.timer);
    this.timer = null;
    this.request?.abort();
    this.request = null;
    if (this.textarea) {
      this.textarea.removeEventListener("input", this.onInput);
      this.textarea.removeEventListener("compositionstart", this.onCompositionStart);
      this.textarea.removeEventListener("compositionend", this.onCompositionEnd);
      this.textarea.removeEventListener("blur", this.onBlur);
      this.textarea.removeEventListener("select", this.onSelectionChange);
      this.textarea.removeEventListener("keyup", this.onSelectionChange);
      this.textarea.removeAttribute("role");
      this.textarea.removeAttribute("aria-autocomplete");
      this.textarea.removeAttribute("aria-controls");
      this.textarea.removeAttribute("aria-expanded");
      this.textarea.removeAttribute("aria-activedescendant");
    }
    if (this.list) {
      this.list.removeEventListener("pointerdown", this.onPointerDown);
      this.list.removeEventListener("click", this.onClick);
      this.list.hidden = true;
      this.list.replaceChildren();
    }
    this.textarea = null;
    this.list = null;
    this.suggestions = [];
    this.context = null;
    this.composing = false;
  }

  input() {
    if (this.composing || !this.textarea || !this.hasCollapsedSelection()) return this.close();
    const currentLine = this.textarea.value.slice(0, this.textarea.selectionStart).split("\n").pop();
    if (currentLine.trimStart().startsWith("/") && !/\s/.test(currentLine.trim())) return this.close();
    const context = composerPathContext(this.textarea.value, this.textarea.selectionStart, { natural: true });
    if (!context) return this.close();
    this.close();
    this.timer = setTimeout(() => this.load(context), this.debounceMs);
  }

  handleKeydown(event) {
    if (this.composing || event.isComposing || !this.textarea || !this.hasCollapsedSelection() || event.ctrlKey || event.metaKey || event.altKey) return false;
    if (!event.shiftKey && event.key === "Tab" && !this.open) {
      const currentLine = this.textarea.value.slice(0, this.textarea.selectionStart).split("\n").pop();
      if (currentLine.trimStart().startsWith("/") && !/\s/.test(currentLine.trim())) return false;
      const context = composerPathContext(this.textarea.value, this.textarea.selectionStart, { force: true });
      if (!context) return false;
      event.preventDefault();
      clearTimeout(this.timer);
      this.load(context);
      return true;
    }
    if (!this.open) return false;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      this.activeIndex = (this.activeIndex + (event.key === "ArrowDown" ? 1 : -1) + this.suggestions.length) % this.suggestions.length;
      this.render();
      return true;
    }
    if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey) {
      event.preventDefault();
      this.accept(this.activeIndex);
      return true;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      this.close();
      return true;
    }
    return false;
  }

  async load(context) {
    if (!this.textarea || this.composing || !this.hasCollapsedSelection()) return;
    this.request?.abort();
    const request = new AbortController();
    const version = ++this.requestVersion;
    this.request = request;
    const body = new URLSearchParams({ session: this.currentSessionPath(), mode: context.mode, query: context.query });
    try {
      const response = await fetch("/composer/path_suggestions", { method: "POST", body, signal: request.signal });
      if (!response.ok) throw new Error("suggestions failed");
      const payload = await response.json();
      if (request.signal.aborted || version !== this.requestVersion || !this.textarea) return;
      const current = composerPathContext(this.textarea.value, this.textarea.selectionStart, { force: context.mode === "path" });
      if (!current || current.mode !== context.mode || current.start !== context.start || current.query !== context.query) return;
      this.context = current;
      this.suggestions = Array.isArray(payload.suggestions) ? payload.suggestions : [];
      this.activeIndex = 0;
      this.render();
    } catch (error) {
      if (error.name !== "AbortError" && version === this.requestVersion) this.close();
    } finally {
      if (this.request === request) this.request = null;
    }
  }

  get open() {
    return !!this.list && !this.list.hidden && this.suggestions.length > 0;
  }

  render() {
    if (!this.list || !this.textarea) return;
    this.list.replaceChildren();
    this.suggestions.forEach((suggestion, index) => {
      const option = this.document.createElement("button");
      option.type = "button";
      option.tabIndex = -1;
      option.id = `${this.list.id}-option-${index}`;
      option.className = "composer-path-option";
      option.dataset.composerPathOption = String(index);
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", index === this.activeIndex ? "true" : "false");
      option.textContent = suggestion.path;
      this.list.append(option);
    });
    this.list.hidden = this.suggestions.length === 0;
    this.textarea.setAttribute("aria-expanded", this.open ? "true" : "false");
    if (this.open) {
      const active = this.list.children[this.activeIndex];
      this.textarea.setAttribute("aria-activedescendant", active.id);
      active.scrollIntoView?.({ block: "nearest" });
    } else {
      this.textarea.removeAttribute("aria-activedescendant");
    }
  }

  hasCollapsedSelection() {
    return !!this.textarea && (this.textarea.selectionEnd ?? this.textarea.selectionStart) === this.textarea.selectionStart;
  }

  currentContextMatches() {
    if (!this.context || !this.textarea || !this.hasCollapsedSelection()) return false;
    const current = composerPathContext(this.textarea.value, this.textarea.selectionStart, { force: this.context.mode === "path" });
    return current?.mode === this.context.mode && current.start === this.context.start && current.query === this.context.query;
  }

  accept(index) {
    const suggestion = this.suggestions[index];
    if (!suggestion || !this.context || !this.textarea || !this.currentContextMatches()) {
      this.close();
      return false;
    }
    const completion = applyComposerPathCompletion(this.textarea.value, this.textarea.selectionStart, this.context, suggestion);
    this.textarea.value = completion.value;
    this.textarea.setSelectionRange(completion.selectionStart, completion.selectionStart);
    this.textarea.dispatchEvent(new Event("input", { bubbles: true }));
    this.close();
    this.textarea.focus();
    return true;
  }

  close() {
    clearTimeout(this.timer);
    this.timer = null;
    this.request?.abort();
    this.request = null;
    this.requestVersion += 1;
    this.suggestions = [];
    this.context = null;
    if (this.list) {
      this.list.hidden = true;
      this.list.replaceChildren();
    }
    if (this.textarea) {
      this.textarea.setAttribute("aria-expanded", "false");
      this.textarea.removeAttribute("aria-activedescendant");
    }
  }
}

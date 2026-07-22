export class FakeEventTarget {
  constructor() { this.listeners = new Map(); }
  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }
  removeEventListener(type, listener) {
    this.listeners.set(type, (this.listeners.get(type) || []).filter((candidate) => candidate !== listener));
  }
}

export class FakeElement extends FakeEventTarget {
  constructor(tagName = "div", selectors = []) {
    super();
    this.tagName = tagName.toUpperCase();
    this.selectors = new Set(selectors);
    this.attributes = new Map();
    this.children = [];
    this.parentElement = null;
    this.dataset = {};
    this.hidden = false;
    this.disabled = false;
    this.tabIndex = 0;
    this.textContent = "";
    this.value = "";
    this.style = { setProperty() {}, removeProperty() {} };
    this.classes = new Set();
    this.classList = {
      add: (...names) => names.forEach((name) => this.classes.add(name)),
      remove: (...names) => names.forEach((name) => this.classes.delete(name)),
      contains: (name) => this.classes.has(name),
      toggle: (name, enabled) => enabled ? this.classes.add(name) : this.classes.delete(name),
    };
  }
  set className(value) { this.classes = new Set(value.split(/\s+/).filter(Boolean)); }
  get className() { return [...this.classes].join(" "); }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.has(name) ? this.attributes.get(name) : null; }
  removeAttribute(name) { this.attributes.delete(name); }
  hasAttribute(name) { return this.attributes.has(name); }
  append(...children) { children.forEach((child) => { child.remove(); child.parentElement = this; this.children.push(child); }); }
  replaceChildren(...children) { this.children.forEach((child) => { child.parentElement = null; }); this.children = []; this.append(...children); }
  remove() {
    if (!this.parentElement) return;
    this.parentElement.children = this.parentElement.children.filter((child) => child !== this);
    this.parentElement = null;
  }
  matches(selector) {
    if (selector.includes(",")) return selector.split(",").some((part) => this.matches(part.trim()));
    if (this.selectors.has(selector) || selector === this.tagName.toLowerCase()) return true;
    if (selector === "[hidden]") return this.hidden;
    if (selector.startsWith(".")) return this.classList.contains(selector.slice(1));
    return false;
  }
  closest(selector) {
    for (let element = this; element; element = element.parentElement) if (element.matches(selector)) return element;
    return null;
  }
  contains(element) { return element === this || this.children.some((child) => child.contains(element)); }
  querySelectorAll(selector) {
    return this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]).filter((child) => child.matches(selector));
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  focus() { this.focused = true; }
  scrollIntoView() {}
  getBoundingClientRect() { return { top: 0, bottom: 20, left: 0, width: 200 }; }
  dispatchEvent() { return true; }
}

export class FakeDocument extends FakeEventTarget {
  constructor() {
    super();
    this.body = new FakeElement("body");
    this.activeElement = null;
  }
  createElement(tagName) { return new FakeElement(tagName); }
  getElementById() { return null; }
  querySelectorAll(selector) { return this.body.querySelectorAll(selector); }
  querySelector(selector) { return this.body.querySelector(selector); }
}

export function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise; });
  return { promise, resolve, reject };
}

export async function settle(predicate, timeout = 2000) {
  const deadline = Date.now() + timeout;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error("timed out waiting for asynchronous frontend work");
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}

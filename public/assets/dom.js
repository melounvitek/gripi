const messageURLPattern = /\bhttps?:\/\/[^\s<>"']+/giu;
const closingURLBrackets = { ")": "(", "]": "[", "}": "{", "）": "（", "］": "［", "｝": "｛" };
const openingURLBrackets = new Set(Object.values(closingURLBrackets));
const trailingURLPunctuation = new Set(".,!?;:…。，、！？；：”’»›」』】》〉");

function linkEnd(text) {
  const balance = Object.fromEntries([...openingURLBrackets].map((opening) => [opening, 0]));
  for (const character of text) {
    if (openingURLBrackets.has(character)) balance[character] += 1;
    const opening = closingURLBrackets[character];
    if (opening) balance[opening] -= 1;
  }

  let end = text.length;
  while (end > 0) {
    const last = text[end - 1];
    if (trailingURLPunctuation.has(last)) {
      end -= 1;
      continue;
    }
    const opening = closingURLBrackets[last];
    if (!opening || balance[opening] >= 0) break;
    balance[opening] += 1;
    end -= 1;
  }
  return end;
}

export function renderTextWithLinks(element, text, document = element?.ownerDocument || globalThis.document) {
  const links = [];
  for (const match of text.matchAll(messageURLPattern)) {
    const end = linkEnd(match[0]);
    const value = match[0].slice(0, end);
    try {
      const url = new URL(value);
      if (!["http:", "https:"].includes(url.protocol) || !url.hostname) continue;
    } catch (_error) {
      continue;
    }
    links.push({ start: match.index, end: match.index + end, value });
  }
  if (links.length === 0) {
    element.textContent = text;
    return;
  }

  const nodes = [];
  let offset = 0;
  for (const link of links) {
    if (link.start > offset) nodes.push(document.createTextNode(text.slice(offset, link.start)));
    const anchor = document.createElement("a");
    anchor.setAttribute("href", link.value);
    anchor.setAttribute("target", "_blank");
    anchor.setAttribute("rel", "nofollow noreferrer noopener");
    anchor.textContent = link.value;
    nodes.push(anchor);
    offset = link.end;
  }
  if (offset < text.length) nodes.push(document.createTextNode(text.slice(offset)));
  element.replaceChildren(...nodes);
}

export function enhanceMessageLinks(root, document = root?.ownerDocument || globalThis.document) {
  root?.querySelectorAll?.(".message--user:not(.message--compact) > .message-body:not(.message-body--markdown)").forEach((body) => {
    renderTextWithLinks(body, body.textContent, document);
  });
}

export function activateToolOutputRegion(body, { focus = false } = {}) {
  if (!body) return;
  body.tabIndex = 0;
  body.setAttribute("role", "region");
  body.setAttribute("aria-label", "Expanded tool output");
  if (focus) body.focus({ preventScroll: true });
}

export function deactivateToolOutputRegion(body) {
  if (!body) return;
  body.tabIndex = -1;
  body.removeAttribute("role");
  body.removeAttribute("aria-label");
}

export function enhanceMarkdownCodeBlocks(root, document = root?.ownerDocument || globalThis.document) {
  root?.querySelectorAll?.(".message-body--markdown pre:not([data-copy-enhanced])").forEach((pre) => {
    pre.dataset.copyEnhanced = "true";
    const wrapper = document.createElement("div");
    wrapper.className = "message-code-block";
    pre.before(wrapper);
    wrapper.append(pre);

    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-button code-block-copy-button";
    button.dataset.copyTarget = "code-block";
    button.textContent = "Copy";
    wrapper.append(button);
  });
}

// The soft edge of .message-body--revealing in app.css.
const REVEAL_EDGE_PIXELS = 32;
const reveals = new WeakMap();

// Wipes in the part of a Markdown body below previousHeight, where a streaming reply's new blocks land.
export function revealGrowth(body, previousHeight) {
  const running = reveals.get(body);
  // A block that lands mid-wipe continues from the edge so far, so nothing above it pops in.
  const edge = running ? parseFloat(body.ownerDocument.defaultView.getComputedStyle(body).getPropertyValue("--reveal-edge")) : Infinity;
  const from = Math.min(edge, previousHeight + REVEAL_EDGE_PIXELS);
  const height = body.offsetHeight;
  running?.cancel();
  body.classList.add("message-body--revealing");
  const duration = Math.min(700, Math.max(280, (height - from) * 1.1));
  const animation = body.animate([{ "--reveal-edge": `${from}px` }, { "--reveal-edge": `${height + REVEAL_EDGE_PIXELS}px` }], { duration, easing: "cubic-bezier(0.25, 0.6, 0.35, 1)", fill: "forwards" });
  reveals.set(body, animation);
  animation.finished.then(() => {
    if (reveals.get(body) !== animation) return;
    reveals.delete(body);
    body.classList.remove("message-body--revealing");
    animation.cancel();
  }, () => {});
}

// Pi-style option lists keep the cursor and the keyboard focus on the same row.
export function movePickerCursor(list, index) {
  const rows = [...list.querySelectorAll('[role="option"]')];
  if (!rows.length) return;
  const selected = (index + rows.length) % rows.length;
  rows.forEach((row, rowIndex) => {
    row.setAttribute("aria-selected", String(rowIndex === selected));
    row.tabIndex = rowIndex === selected ? 0 : -1;
  });
  rows[selected].focus();
}

// Pi-style filter lists keep the keyboard focus in the input, which points at the row under the cursor.
export function pointPickerCursor(input, options, index) {
  options.forEach((option, optionIndex) => option.setAttribute("aria-selected", String(optionIndex === index)));
  if (!options[index]) return input.removeAttribute("aria-activedescendant");
  input.setAttribute("aria-activedescendant", options[index].id);
  options[index].scrollIntoView({ block: "nearest" });
}

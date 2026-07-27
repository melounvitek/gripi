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

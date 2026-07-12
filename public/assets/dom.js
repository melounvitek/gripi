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

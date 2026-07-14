const { contextBridge, ipcRenderer } = require("electron");

function copyTargetText(button) {
  if (button.dataset.copyTarget === "code-block") {
    const block = button.closest(".message-code-block")?.querySelector("pre");
    return block?.innerText || block?.textContent;
  }

  const body = button.closest(".message")?.querySelector(".message-body");
  if (!body) return "";
  if (body.dataset.plainText) return body.dataset.plainText;

  const clone = body.cloneNode(true);
  clone.querySelectorAll?.(".code-block-copy-button").forEach((copyButton) => copyButton.remove());
  return clone.innerText || clone.textContent;
}

async function copyText(text) {
  const result = await ipcRenderer.invoke("gateway-clipboard:write", text);
  return Boolean(result?.ok);
}

async function handleCopyClick(event) {
  const button = event.target.closest?.("[data-copy-target]");
  if (!button) return;

  const text = copyTargetText(button);
  if (!text) return;

  event.preventDefault();
  event.stopImmediatePropagation();

  const original = button.textContent;
  try {
    button.textContent = await copyText(text) ? "Copied" : "Copy failed";
  } catch (_error) {
    button.textContent = "Copy failed";
  }
  setTimeout(() => { button.textContent = original; }, 1200);
}

document.addEventListener("click", handleCopyClick, true);

contextBridge.exposeInMainWorld("gripiElectron", {
  copyText: (text) => ipcRenderer.invoke("gateway-clipboard:write", text),
  showNotification: (payload) => ipcRenderer.invoke("gateway-notification:show", payload)
});

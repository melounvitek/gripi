export function keyboardScrollKey(event) {
  return ["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " ", "Spacebar"].includes(event.key);
}

export function sessionSearchShortcut(event) {
  if (String(event.key || "").toLowerCase() !== "f") return false;
  if (event.altKey || !event.shiftKey) return false;
  return !!(event.ctrlKey || event.metaKey);
}

export function currentSessionFindNavigationShortcut(event) {
  if (String(event.key || "").toLowerCase() !== "g") return null;
  if (event.altKey || !(event.ctrlKey || event.metaKey)) return null;
  return event.shiftKey ? -1 : 1;
}

export function recentSessionShortcutFromEvent(event) {
  if (/^[1-9]$/.test(event.key)) return event.key;
  return event.code.match(/^Digit([1-9])$/)?.[1] || event.code.match(/^Numpad([1-9])$/)?.[1] || null;
}

export function isCtrlOrMetaShortcut(event, key) {
  return (event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === key;
}

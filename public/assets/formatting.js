export function compactNumber(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return String(value || "");
  if (number < 1000) return String(Math.round(number));
  if (number < 1000000) return `${(number / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  return `${(number / 1000000).toFixed(1).replace(/\.0$/, "")}M`;
}

export function formatWaitDuration(milliseconds) {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
}

export function imageAttachmentLabel(count) {
  return `${count} image${count === 1 ? "" : "s"} attached`;
}

export function sessionNameSlashCommand(message) {
  return /^\/name(?:[ \t]+[^\r\n]+)?$/.test(message.trim());
}

export function sessionCompactSlashCommand(message) {
  return /^\/compact(?:[ \t]+[^\r\n]+)?$/.test(message.trim());
}

export function sessionExportSlashCommand(message) {
  const match = message.trim().match(/^\/export(?:[ \t]+([^\r\n]+))?$/);
  if (!match) return null;

  let filename = match[1]?.trim() || "";
  if (filename.length >= 2 && ["\"", "'"].includes(filename[0]) && filename.at(-1) === filename[0]) {
    filename = filename.slice(1, -1).trim();
  }
  return { filename };
}

export function sessionForkSlashCommand(message) {
  return /^\/fork$/.test(message.trim());
}

export function sessionTreeSlashCommand(message) {
  return /^\/tree$/.test(message.trim());
}

export function sessionCloneSlashCommand(message) {
  return /^\/clone$/.test(message.trim());
}

export function sessionNewSlashCommand(message) {
  return /^\/new$/.test(message.trim());
}

export function sessionModelSlashCommand(message) {
  return /^\/model$/.test(message.trim());
}

export function sessionAuthGuidanceSlashCommand(message) {
  const trimmed = message.trim();
  if (/^\/login(?: +[^\r\n]+)?$/.test(trimmed)) return "login";
  if (trimmed === "/logout") return "logout";
  return null;
}

export function sessionNameFromEvent(event) {
  return ["session_info", "session_info_changed"].includes(event.type) ? event.name : null;
}

function protectFencedNotificationCode(source, protect) {
  const lines = source.split("\n");
  const output = [];
  for (let index = 0; index < lines.length; index += 1) {
    const opening = lines[index].match(/^\s{0,3}(`{3,}|~{3,})(.*)$/);
    if (!opening || (opening[1][0] === "`" && opening[2].includes("`"))) {
      output.push(lines[index]);
      continue;
    }

    let closingIndex = index + 1;
    while (closingIndex < lines.length) {
      const closing = lines[closingIndex].match(/^\s{0,3}(`{3,}|~{3,})[ \t]*$/);
      if (closing && closing[1][0] === opening[1][0] && closing[1].length >= opening[1].length) break;
      closingIndex += 1;
    }

    output.push(protect(lines.slice(index + 1, closingIndex).join("\n")));
    index = closingIndex;
  }
  return output.join("\n");
}

function protectInlineNotificationCode(source, protect) {
  const runs = [];
  for (let index = 0; index < source.length; index += 1) {
    if (source[index] !== "`") continue;

    let end = index + 1;
    while (source[end] === "`") end += 1;
    runs.push({ start: index, end, length: end - index });
    index = end - 1;
  }

  const nextMatchingRun = new Array(runs.length);
  const nextByLength = new Map();
  for (let index = runs.length - 1; index >= 0; index -= 1) {
    nextMatchingRun[index] = nextByLength.get(runs[index].length);
    nextByLength.set(runs[index].length, index);
  }

  let output = "";
  let cursor = 0;
  for (let index = 0; index < runs.length; index += 1) {
    if (runs[index].start < cursor || nextMatchingRun[index] == null) continue;

    const closing = runs[nextMatchingRun[index]];
    output += source.slice(cursor, runs[index].start) + protect(source.slice(runs[index].end, closing.start));
    cursor = closing.end;
    index = nextMatchingRun[index];
  }
  return output + source.slice(cursor);
}

function stripNotificationEmphasis(source) {
  const nestedPatterns = [
    /(^|[\s([{>"'])\*\*(?=\S)([^*\n]*?)\*([^*\n]+)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])\*(?=\S)([^*\n]*?)\*\*([^*\n]+)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
  ];
  const patterns = [
    /(^|[\s([{>"'])\*\*\*(?=\S)([^*\n]*?\S)\*\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])___(?=\S)([^_\n]*?\S)___(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])~~(?=\S)([^~\n]*?\S)~~(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])\*\*(?=\S)([^*\n]*?\S)\*\*(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])__(?=\S)([^_\n]*?\S)__(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])\*(?=\S)([^*\n]*?\S)\*(?=$|[\s)\]}>,.!?;:"'])/gm,
    /(^|[\s([{>"'])_(?=\S)([^_\n]*?\S)_(?=$|[\s)\]}>,.!?;:"'])/gm,
  ];

  for (let pass = 0; pass < 3; pass += 1) {
    const previous = source;
    nestedPatterns.forEach((pattern) => { source = source.replace(pattern, "$1$2$3"); });
    patterns.forEach((pattern) => { source = source.replace(pattern, "$1$2"); });
    if (source === previous) break;
  }
  return source;
}

export function notificationReplyPreview(text) {
  const source = String(text || "").replace(/\r\n?/g, "\n");
  const protectedSegments = [];
  const sourceCharacters = new Set(source);
  let markerCodePoint = 0xE000;
  while (markerCodePoint <= 0x10FFFF && sourceCharacters.has(String.fromCodePoint(markerCodePoint))) markerCodePoint += 1;
  let segmentMarker = markerCodePoint <= 0x10FFFF ? String.fromCodePoint(markerCodePoint) : "\uE000\uE001";
  while (source.includes(segmentMarker)) segmentMarker += "\uE001";
  const protect = (value) => `${segmentMarker}${protectedSegments.push(value) - 1}${segmentMarker}`;

  let preview = protectFencedNotificationCode(source, protect);
  preview = protectInlineNotificationCode(preview, protect)
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]+\)/g, "$1")
    .replace(/<(https?:\/\/[^>\s]+)>/gi, (_match, url) => protect(url))
    .replace(/\bhttps?:\/\/[^\s<]+/gi, (url) => protect(url))
    .replace(/^\s{0,3}>\s?/gm, "")
    .replace(/^\s{0,3}#{1,6}\s+/gm, "")
    .replace(/^\s*(?:[-*+] |\d+[.)]\s+)/gm, "")
    .replace(/^\s*[-*_]{3,}\s*$/gm, " ")
    .replace(/<\/?[a-z][^>]*>/gi, " ")
    .replace(/\bjavascript:/gi, "");
  preview = stripNotificationEmphasis(preview);
  const protectedSegmentPattern = new RegExp(`${segmentMarker}(\\d+)${segmentMarker}`, "g");
  for (let pass = 0; pass <= protectedSegments.length; pass += 1) {
    const restored = preview.replace(protectedSegmentPattern, (_match, index) => protectedSegments[Number(index)]);
    if (restored === preview) break;
    preview = restored;
  }
  preview = preview.replace(/\s+/g, " ").trim();
  if (!preview) return "New reply.";

  const characters = Array.from(preview);
  return characters.length > 180 ? `${characters.slice(0, 177).join("")}…` : preview;
}

export function normalizedMessageText(text) {
  return String(text || "").replace(/\r\n?/g, "\n").trim();
}

export function stableTextHash(text) {
  const bytes = new TextEncoder().encode(text);
  let hash = 5381;
  bytes.forEach((byte) => { hash = (((hash << 5) + hash) + byte) >>> 0; });
  return hash.toString(16);
}

export function messageTimestampKey(timestamp) {
  if (timestamp === null || timestamp === undefined) return "";
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  return String(Math.floor(date.getTime() / 1000));
}

export function messageRoleKey(roleName) {
  if (["assistant", "user", "error"].includes(roleName)) return roleName;
  if (["tool", "toolResult", "bashExecution"].includes(roleName)) return "tool";
  return "status";
}

export function messageFingerprint(roleName, text, timestampKey) {
  if (!timestampKey) return "";
  return `${messageRoleKey(roleName)}:${timestampKey}:${stableTextHash(normalizedMessageText(text))}`;
}

export function messageRoleLabel(roleName) {
  if (roleName === "assistant") return "pi";
  if (roleName === "toolResult") return "tool result";
  if (roleName === "bashExecution") return "shell";
  if (["custom", "session_info"].includes(roleName)) return "status";
  return roleName || "status";
}

export function extensionUiRequestNotice(event) {
  if (event?.type !== "extension_ui_request") return null;
  if (["select", "confirm", "input", "editor"].includes(event.method)) return null;
  if (event.method === "notify" && event.message) {
    if (event.notifyType === "error") return { role: "error", text: event.message };
    return { role: "status", text: event.notifyType === "warning" ? `Warning: ${event.message}` : event.message };
  }
  return null;
}

export function eventStatusText(event) {
  if (["session_info", "session_info_changed"].includes(event.type) && event.name) return `Session renamed to “${event.name}”`;
  if (event.type === "custom_message" && event.content) return event.content;
  if (event.type === "custom" && event.customType) return `${event.customType} updated`;
  if (event.type === "queue_update") return "Queued messages updated";
  if (event.type === "compaction_start") return "Compaction started";
  if (event.type === "compaction_end") return event.aborted ? "Compaction aborted" : "Compaction finished";
  return event.message || event.text || event.type || "Status update";
}

export function formatTimestamp(timestamp, fallbackToNow = true) {
  const date = timestamp !== null && timestamp !== undefined ? new Date(timestamp) : (fallbackToNow ? new Date() : null);
  if (!date || Number.isNaN(date.getTime())) return "";
  const pad = (value) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function eventTimestamp(event) {
  return event?.gatewayTimestamp ?? event?.timestamp ?? event?.message?.timestamp ?? event?.delta?.timestamp ?? event?.item?.timestamp;
}

export function errorValueText(value) {
  if (!value) return "";
  if (typeof value === "string") return value.trim();
  if (typeof value !== "object") return "";
  return errorValueText(value.error) ||
    errorValueText(value.finalError) ||
    errorValueText(value.message) ||
    errorValueText(value.text) ||
    errorValueText(value.details?.error) ||
    errorValueText(value.details?.message);
}

export function eventErrorText(event) {
  if (!event || typeof event !== "object") return "";
  const errorText = errorValueText(event.error) || errorValueText(event.finalError) || errorValueText(event.errorMessage);
  if (event.type === "extension_error" && event.extensionPath === "command:sessions" && event.event === "command" && errorText === "Cannot read properties of undefined (reading 'action')") {
    return "This extension command requires terminal UI that Gripi does not support yet.";
  }
  if (errorText) return errorText;
  if (event.type === "error" || /(?:error|fail(?:ed|ure)?)/i.test(event.type || "")) return errorValueText(event);
  return "";
}

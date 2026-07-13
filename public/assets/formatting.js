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
  const trimmed = message.trim();
  if (/^\/(?:name|rename)$/.test(trimmed)) return { valid: false };
  if (/^\/(?:name|rename)[ \t]+[^\r\n]+$/.test(trimmed)) return { valid: true };
  return null;
}

export function sessionCompactSlashCommand(message) {
  return /^\/compact(?:[ \t]+[^\r\n]+)?$/.test(message.trim());
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

export function sessionTitleFromEvent(event) {
  if (event.type === "session_info") return event.name;
  if (event.type === "custom" && event.customType === "pi-extensions-session-title") return event.data?.title;
  if (event.type === "custom_message" && event.customType === "session-title-update") {
    return String(event.content || "").match(/^Session renamed to: `(.+)`$/)?.[1];
  }
  return null;
}

export function notificationReplyPreview(text) {
  const preview = String(text || "").replace(/\s+/g, " ").trim();
  if (!preview) return "New reply.";
  return preview.length > 180 ? `${preview.slice(0, 177)}…` : preview;
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
  if (!timestamp) return "";
  const date = new Date(timestamp);
  if (Number.isNaN(date.getTime())) return "";
  return String(Math.floor(date.getTime() / 1000));
}

export function messageRoleKey(roleName) {
  if (["assistant", "user", "error"].includes(roleName)) return roleName;
  if (["tool", "toolResult"].includes(roleName)) return "tool";
  return "status";
}

export function messageFingerprint(roleName, text, timestampKey) {
  if (!timestampKey) return "";
  return `${messageRoleKey(roleName)}:${timestampKey}:${stableTextHash(normalizedMessageText(text))}`;
}

export function messageRoleLabel(roleName) {
  if (roleName === "assistant") return "pi";
  if (roleName === "toolResult") return "tool result";
  if (["custom", "session_info"].includes(roleName)) return "status";
  return roleName || "status";
}

export function eventStatusText(event) {
  if (event.type === "session_info" && event.name) return `Session renamed to “${event.name}”`;
  if (event.type === "custom_message" && event.content) return event.content;
  if (event.type === "custom" && event.customType) return `${event.customType} updated`;
  if (event.type === "queue_update") return "Queued follow-up work updated";
  if (event.type === "compaction_start") return "Compaction started";
  if (event.type === "compaction_end") return event.aborted ? "Compaction aborted" : "Compaction finished";
  return event.message || event.text || event.type || "Status update";
}

export function formatTimestamp(timestamp, fallbackToNow = true) {
  const date = timestamp ? new Date(timestamp) : (fallbackToNow ? new Date() : null);
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
  const errorText = errorValueText(event.error) || errorValueText(event.finalError);
  if (errorText) return errorText;
  if (event.type === "error" || /(?:error|fail(?:ed|ure)?)/i.test(event.type || "")) return errorValueText(event);
  return "";
}

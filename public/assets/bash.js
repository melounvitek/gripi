export function parseNativeBash(message) {
  const raw = String(message || "");
  if (!raw.startsWith("!")) return null;

  const excludeFromContext = raw.startsWith("!!");
  const command = raw.slice(excludeFromContext ? 2 : 1).trim();
  return command ? { command, excludeFromContext } : null;
}

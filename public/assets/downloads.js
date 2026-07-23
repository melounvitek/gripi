export async function downloadResponse(response, fallbackFilename, options = {}) {
  const document = options.document || globalThis.document;
  const urls = options.urls || globalThis.URL;
  const blob = await response.blob();
  if (options.cancelled?.()) return null;

  const filename = downloadFilename(response.headers.get("Content-Disposition"), fallbackFilename);
  const objectUrl = urls.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectUrl;
  anchor.download = filename;
  anchor.hidden = true;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => urls.revokeObjectURL(objectUrl), 0);
  return filename;
}

function downloadFilename(disposition, fallback) {
  const encoded = disposition?.match(/filename\*=utf-8''([^;]+)/i)?.[1];
  if (encoded) {
    try {
      return decodeURIComponent(encoded);
    } catch (_error) {
    }
  }

  const quoted = disposition?.match(/filename="((?:\\.|[^"])*)"/i)?.[1];
  if (quoted) return quoted.replace(/\\(.)/g, "$1");

  const unquoted = disposition?.match(/(?:^|;)\s*filename=([^;]+)/i)?.[1]?.trim();
  return unquoted || fallback;
}

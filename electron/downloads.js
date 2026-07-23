const path = require("node:path");

function configureSessionExportDownload(item, allowedOrigin, downloadsDirectory) {
  if (!sameOrigin(item.getURL(), allowedOrigin)) return false;
  if (item.getMimeType().split(";", 1)[0].trim().toLowerCase() !== "text/html") return false;

  const filename = path.basename(item.getFilename().replaceAll("\\", "/"));
  if (!filename.toLowerCase().endsWith(".html")) return false;

  item.setSaveDialogOptions({
    title: "Save Session Export",
    defaultPath: path.join(downloadsDirectory, filename),
    filters: [{ name: "HTML", extensions: ["html"] }]
  });
  return true;
}

function sameOrigin(candidateUrl, allowedOrigin) {
  try {
    return new URL(candidateUrl).origin === allowedOrigin;
  } catch (_error) {
    return false;
  }
}

module.exports = { configureSessionExportDownload };

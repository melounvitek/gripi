const assert = require("node:assert/strict");
const path = require("node:path");
const test = require("node:test");
const { configureSessionExportDownload } = require("./downloads");

test("configures a native save dialog for same-origin HTML exports", () => {
  let options;
  const item = {
    getURL: () => "blob:https://gripi.example/export-id",
    getMimeType: () => "text/html",
    getFilename: () => "Quarterly report.html",
    setSaveDialogOptions(value) { options = value; }
  };

  assert.equal(configureSessionExportDownload(item, "https://gripi.example", "/home/user/Downloads"), true);
  assert.deepEqual(options, {
    title: "Save Session Export",
    defaultPath: path.join("/home/user/Downloads", "Quarterly report.html"),
    filters: [{ name: "HTML", extensions: ["html"] }]
  });
});

test("does not configure unrelated or cross-origin downloads", () => {
  let configured = false;
  const item = {
    getURL: () => "blob:https://other.example/export-id",
    getMimeType: () => "text/html",
    getFilename: () => "session.html",
    setSaveDialogOptions() { configured = true; }
  };

  assert.equal(configureSessionExportDownload(item, "https://gripi.example", "/tmp"), false);
  item.getURL = () => "https://gripi.example/archive.zip";
  item.getMimeType = () => "application/zip";
  assert.equal(configureSessionExportDownload(item, "https://gripi.example", "/tmp"), false);
  assert.equal(configured, false);
});

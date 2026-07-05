const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");

const root = path.join(__dirname, "..");

function read(relativePath) {
  return fs.readFileSync(path.join(root, relativePath), "utf8");
}

test("desktop window does not enforce a larger minimum size than a browser tab", () => {
  const main = read("electron/main.js");

  assert.doesNotMatch(main, /minWidth\s*:/);
  assert.doesNotMatch(main, /minHeight\s*:/);
});

test("desktop window hides the native menu bar by default", () => {
  const main = read("electron/main.js");

  assert.match(main, /autoHideMenuBar:\s*true/);
});

test("same-origin gateway popups open in an Electron window", () => {
  const main = read("electron/main.js");

  assert.match(main, /if \(sameOrigin\(url, allowedOrigin\)\) return openSameOriginPopupWindow\(url, allowedOrigin, partition\);/);
  assert.match(main, /function openSameOriginPopupWindow\(url, allowedOrigin, partition\)/);
});

test("desktop keeps popup windows alive until they close", () => {
  const main = read("electron/main.js");

  assert.match(main, /const popupWindows = new Set\(\);/);
  assert.match(main, /popupWindows\.add\(popupWindow\);/);
  assert.match(main, /popupWindow\.on\("closed", \(\) => popupWindows\.delete\(popupWindow\)\);/);
});

test("desktop shell chrome remains usable in narrow or short windows", () => {
  const html = read("electron/shell.html");

  assert.match(html, /#tabs\s*{[^}]*overflow-x:\s*auto;/s);
  assert.match(html, /#tabs\s*{[^}]*flex-shrink:\s*0;/s);
  assert.match(html, /\.tab\s*{[^}]*flex:\s*0 1 220px;/s);
  assert.match(html, /\.panel\s*{[^}]*overflow:\s*auto;/s);
  assert.match(html, /\.card\s*{[^}]*box-sizing:\s*border-box;/s);
  assert.match(html, /@media \(max-height: 520px\)/);
});

test("desktop gateway webviews allow target blank popups", () => {
  const shell = read("electron/shell.js");

  assert.match(shell, /webview\.setAttribute\("allowpopups", ""\);/);
});

test("desktop shell does not reset an existing gateway webview after in-app navigation", () => {
  const shell = read("electron/shell.js");

  assert.match(shell, /existingWebview\.dataset\.gatewayUrl === gateway\.url/);
  assert.match(shell, /sameUrlOrigin\(existingWebview\.dataset\.gatewayUrl, gateway\.url\)/);
  assert.doesNotMatch(shell, /existingWebview\.src !== gateway\.url/);
});

test("desktop server management uses server wording", () => {
  const main = read("electron/main.js");
  const shell = read("electron/shell.js");

  assert.match(main, /Add Server…/);
  assert.match(main, /Remove Current Server…/);
  assert.match(shell, /Add Server/);
  assert.match(shell, /New Server/);
  assert.match(shell, /Server URL/);
  assert.match(shell, /Remove server/);
  assert.doesNotMatch(shell, /Add Gateway|New Gateway|Gateway URL|Remove gateway/);
});

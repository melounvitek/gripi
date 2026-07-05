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

test("desktop shell chrome remains usable in narrow or short windows", () => {
  const html = read("electron/shell.html");

  assert.match(html, /#tabs\s*{[^}]*overflow-x:\s*auto;/s);
  assert.match(html, /#tabs\s*{[^}]*flex-shrink:\s*0;/s);
  assert.match(html, /\.tab\s*{[^}]*flex:\s*0 1 220px;/s);
  assert.match(html, /\.panel\s*{[^}]*overflow:\s*auto;/s);
  assert.match(html, /\.card\s*{[^}]*box-sizing:\s*border-box;/s);
  assert.match(html, /@media \(max-height: 520px\)/);
});

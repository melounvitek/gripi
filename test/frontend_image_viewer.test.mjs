import assert from "node:assert/strict";
import { test } from "node:test";

import { ImageViewerController } from "../public/assets/image_viewer_controller.js";
import { FakeDocument, FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

function viewerFixture() {
  const document = new FakeDocument();
  const window = new FakeEventTarget();
  window.addEventListener = FakeEventTarget.prototype.addEventListener;
  window.removeEventListener = FakeEventTarget.prototype.removeEventListener;
  window.mutationObservers = [];
  window.MutationObserver = class {
    constructor(callback) { this.callback = callback; window.mutationObservers.push(this); }
    observe(target) { this.target = target; }
    disconnect() { this.target = null; }
  };

  const overlay = new FakeElement("div", ["[data-image-viewer]"]);
  overlay.hidden = true;
  const stage = new FakeElement("div", ["[data-image-viewer-stage]"]);
  stage.clientWidth = 800;
  stage.clientHeight = 600;
  stage.setPointerCapture = () => {};
  stage.releasePointerCapture = () => {};
  const image = new FakeElement("img", ["[data-image-viewer-image]"]);
  image.complete = true;
  image.naturalWidth = 1600;
  image.naturalHeight = 1200;
  const close = new FakeElement("button", ["[data-image-viewer-close]"]);
  const previous = new FakeElement("button", ["[data-image-viewer-previous]"]);
  const next = new FakeElement("button", ["[data-image-viewer-next]"]);
  const position = new FakeElement("output", ["[data-image-viewer-position]"]);
  const zoomOut = new FakeElement("button", ["[data-image-viewer-zoom-out]"]);
  const zoomValue = new FakeElement("output", ["[data-image-viewer-zoom-value]"]);
  const zoomIn = new FakeElement("button", ["[data-image-viewer-zoom-in]"]);
  const actualSize = new FakeElement("button", ["[data-image-viewer-actual-size]"]);
  const fit = new FakeElement("button", ["[data-image-viewer-fit]"]);
  const download = new FakeElement("a", ["[data-image-viewer-download]"]);
  stage.append(image);
  overlay.append(stage, close, previous, next, position, zoomOut, zoomValue, zoomIn, actualSize, fit, download);
  document.body.append(overlay);

  const controller = new ImageViewerController(document, window);
  controller.bind();
  return { controller, document, window, overlay, stage, image, close, previous, next, position, zoomOut, zoomValue, zoomIn, download };
}

function conversationImages(document, sources) {
  let conversation = document.querySelector("#conversation-scroll");
  if (!conversation) {
    conversation = new FakeElement("section", ["#conversation-scroll"]);
    document.body.append(conversation);
  }
  return sources.map((src) => {
    const article = new FakeElement("article");
    const button = new FakeElement("button", ["[data-image-viewer-open]"]);
    const image = new FakeElement("img");
    image.src = src;
    button.append(image);
    article.append(button);
    conversation.append(article);
    return { button, image };
  });
}

function click(element) {
  element.listeners.get("click").forEach((listener) => listener({ target: element, preventDefault() {} }));
}

function pressKey(document, key) {
  document.listeners.get("keydown").forEach((listener) => listener({ key, preventDefault() {} }));
}

function sourceImage(src = "/attachments/image.png", alt = "Attached image") {
  const article = new FakeElement("article");
  const container = new FakeElement("div");
  const button = new FakeElement("button", ["[data-image-viewer-open]"]);
  const image = new FakeElement("img");
  image.src = src;
  image.currentSrc = image.src;
  image.alt = alt;
  image.naturalWidth = 1600;
  image.naturalHeight = 1200;
  const firstButton = new FakeElement("button", ["[data-image-viewer-open]"]);
  button.append(image);
  container.append(firstButton, button);
  article.dataset.messageFingerprint = "user:image";
  article.append(container);
  return { article, container, button, image };
}

function pointer(pointerId, clientX, clientY, target = null) {
  return {
    pointerId,
    clientX,
    clientY,
    target,
    preventDefault() {},
  };
}

test("image viewer opens fitted, exposes actual size, and restores focus when closed", () => {
  const { controller, document, overlay, close, zoomOut, zoomValue, zoomIn, download } = viewerFixture();
  const { article, button, image } = sourceImage();
  document.body.append(article);

  controller.open(image, button);

  assert.equal(controller.isOpen, true);
  assert.equal(overlay.hidden, false);
  assert.equal(controller.scale, 0.42);
  assert.equal(zoomValue.textContent, "42%");
  assert.equal(download.getAttribute("href"), "/attachments/image.png");
  assert.equal(download.getAttribute("download"), "image.png");
  assert.equal(close.focused, true);
  assert.equal(zoomOut.getAttribute("aria-disabled"), "true");
  article.dataset.messageFingerprint = "user:image:acknowledged";

  close.focused = false;
  document.activeElement = download;
  let trapped = false;
  controller.trapFocus({ shiftKey: false, preventDefault() { trapped = true; } });
  assert.equal(trapped, true);
  assert.equal(close.focused, true);

  controller.showActualSize();
  assert.equal(controller.scale, 1);
  assert.equal(zoomValue.textContent, "100%");
  assert.deepEqual([zoomOut.getAttribute("aria-disabled"), zoomIn.getAttribute("aria-disabled")], ["false", "false"]);
  controller.zoomBy(4);
  assert.equal(zoomIn.getAttribute("aria-disabled"), "true");

  const replacementArticle = new FakeElement("article");
  const firstReplacement = new FakeElement("button", ["[data-image-viewer-open]"]);
  const replacement = new FakeElement("button", ["[data-image-viewer-open]"]);
  replacementArticle.dataset.messageFingerprint = article.dataset.messageFingerprint;
  replacementArticle.append(firstReplacement, replacement);
  article.remove();
  article.isConnected = false;
  button.isConnected = false;
  document.body.append(replacementArticle);
  controller.close();
  assert.equal(controller.isOpen, false);
  assert.equal(overlay.hidden, true);
  assert.equal(download.getAttribute("href"), null);
  assert.equal(download.getAttribute("download"), null);
  assert.equal(button.focused, undefined);
  assert.equal(replacement.focused, true);
});

test("image viewer steps through the conversation images with arrow buttons and keys", () => {
  const { controller, document, close, previous, next, position, download } = viewerFixture();
  const [first, second] = conversationImages(document, ["/one.png", "/two.png", "/three.png"]);

  controller.open(second.image, second.button);
  assert.equal(position.textContent, "2 / 3");
  assert.deepEqual([previous.hidden, next.hidden, position.hidden], [false, false, false]);
  assert.deepEqual([previous.getAttribute("aria-disabled"), next.getAttribute("aria-disabled")], ["false", "false"]);

  controller.showActualSize();
  close.focused = false;
  click(next);
  assert.equal(download.getAttribute("href"), "/three.png");
  assert.equal(position.textContent, "3 / 3");
  assert.equal(next.getAttribute("aria-disabled"), "true");
  click(next);
  assert.equal(download.getAttribute("href"), "/three.png");
  assert.equal(controller.scale, controller.fitScale);
  assert.equal(close.focused, false);

  pressKey(document, "ArrowRight");
  assert.equal(download.getAttribute("href"), "/three.png");
  pressKey(document, "ArrowLeft");
  pressKey(document, "ArrowLeft");
  assert.equal(download.getAttribute("href"), "/one.png");
  assert.equal(position.textContent, "1 / 3");
  assert.equal(previous.getAttribute("aria-disabled"), "true");

  controller.close();
  assert.equal(first.button.focused, true);
});

test("image viewer offers navigation once another conversation image arrives", () => {
  const { controller, document, window, previous, next, position, download } = viewerFixture();
  const [only] = conversationImages(document, ["/one.png"]);

  controller.open(only.image, only.button);
  assert.deepEqual([previous.hidden, next.hidden, position.hidden], [true, true, true]);
  pressKey(document, "ArrowRight");
  assert.equal(download.getAttribute("href"), "/one.png");

  conversationImages(document, ["/two.png"]);
  window.mutationObservers.filter((observer) => observer.target).forEach((observer) => observer.callback([]));
  assert.deepEqual([previous.hidden, next.hidden, position.hidden], [false, false, false]);
  assert.equal(next.getAttribute("aria-disabled"), "false");
  assert.equal(position.textContent, "1 / 2");
});

test("image viewer uses the displayed image format for its download filename", () => {
  const { controller, download } = viewerFixture();
  const { button, image } = sourceImage("data:image/png;base64,cG5n", "preview.jpg");

  controller.open(image, button);

  assert.equal(download.getAttribute("download"), "preview.png");
});

test("image viewer downloads read results using the tool path basename", () => {
  for (const [path, expected] of [
    ["/tmp/screenshot.png", "screenshot.png"],
    ["/tmp/my screenshot.png", "my screenshot.png"],
    ["C:\\images\\screenshot.png", "screenshot.png"],
    ["relative/preview.jpg", "preview.png"],
    ["/tmp/preview?.png", "preview-.png"],
    ["", "image.png"],
  ]) {
    const { controller, download } = viewerFixture();
    const { article, button, image } = sourceImage("data:image/png;base64,cG5n");
    const summary = new FakeElement("span", [".compact-summary"]);
    const command = new FakeElement("span", [".tool-command"]);
    command.textContent = "read";
    const toolPath = new FakeElement("span", [".tool-path"]);
    toolPath.textContent = path;
    summary.append(command, toolPath);
    article.append(summary);

    controller.open(image, button);

    assert.equal(download.getAttribute("download"), expected, path);
  }
});

test("image viewer ignores paths outside its own read tool summary", () => {
  for (const toolName of ["read", "write"]) {
    const { controller, download } = viewerFixture();
    const { article, button, image } = sourceImage();
    const summary = new FakeElement("span", [".compact-summary"]);
    const command = new FakeElement("span", [".tool-command"]);
    command.textContent = toolName;
    const toolPath = new FakeElement("span", [".tool-path"]);
    toolPath.textContent = "/tmp/unrelated.png";
    summary.append(command);
    if (toolName === "write") summary.append(toolPath);
    else article.append(toolPath);
    article.append(summary);

    controller.open(image, button);

    assert.equal(download.getAttribute("download"), "image.png");
  }
});

test("image viewer retains its live object URL until close", () => {
  const { controller, window } = viewerFixture();
  const { button, image } = sourceImage("blob:image-preview", "preview.png");
  const revoked = [];
  window.URL = { revokeObjectURL: (url) => revoked.push(url) };

  controller.open(image, button);

  assert.equal(controller.retainObjectURL("blob:other"), false);
  assert.equal(controller.retainObjectURL("blob:image-preview"), true);
  controller.close();
  assert.deepEqual(revoked, ["blob:image-preview"]);
});

test("image viewer pans one pointer and pinches around the moving midpoint", () => {
  const { controller } = viewerFixture();
  const { button, image } = sourceImage();
  controller.open(image, button);
  controller.showActualSize();

  controller.handlePointerDown(pointer(1, 400, 300, controller.image));
  controller.handlePointerUp(pointer(1, 400, 300, controller.image));
  controller.handleViewerClick({ target: controller.stage, preventDefault() {} });
  assert.equal(controller.isOpen, true);

  controller.handlePointerDown(pointer(1, 400, 300, controller.image));
  controller.handlePointerMove(pointer(1, 500, 350, controller.image));
  controller.handlePointerUp(pointer(1, 500, 350, controller.image));
  assert.equal(controller.offsetX, 100);
  assert.equal(controller.offsetY, 50);

  controller.resetPosition();
  controller.handlePointerDown(pointer(1, 300, 300, controller.image));
  controller.handlePointerDown(pointer(2, 500, 300, controller.image));
  controller.handlePointerMove(pointer(2, 600, 300, controller.image));
  assert.equal(controller.scale, 1.5);
  assert.equal(controller.offsetX, 50);
  assert.equal(controller.offsetY, 0);

  controller.handlePointerUp(pointer(2, 600, 300, controller.image));
  controller.handlePointerUp(pointer(1, 300, 300, controller.image));
  controller.handleViewerClick({ target: controller.stage, preventDefault() {} });
  assert.equal(controller.isOpen, true);

  controller.handleDoubleClick({ preventDefault() {} });
  assert.equal(controller.scale, controller.fitScale);
  controller.handlePointerDown(pointer(3, 10, 10, controller.stage));
  controller.handlePointerUp(pointer(3, 10, 10, controller.stage));
  controller.handleViewerClick({ target: controller.stage, preventDefault() {} });
  assert.equal(controller.isOpen, false);
});

test("image viewer wheel zoom stays bounded and backdrop click closes without a second tap", () => {
  const { controller, stage } = viewerFixture();
  const { button, image } = sourceImage();
  controller.open(image, button);
  let prevented = false;

  controller.handleWheel({
    clientX: 400,
    clientY: 300,
    deltaY: -100,
    preventDefault() { prevented = true; },
  });

  assert.equal(prevented, true);
  assert.ok(controller.scale > controller.fitScale);

  controller.handleViewerClick({ target: stage, preventDefault() {} });
  assert.equal(controller.isOpen, false);
});

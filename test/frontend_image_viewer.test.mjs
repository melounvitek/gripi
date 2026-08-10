import assert from "node:assert/strict";
import { test } from "node:test";

import { ImageViewerController } from "../public/assets/image_viewer_controller.js";
import { FakeDocument, FakeElement, FakeEventTarget } from "./helpers/fake_dom.mjs";

function viewerFixture() {
  const document = new FakeDocument();
  const window = new FakeEventTarget();
  window.addEventListener = FakeEventTarget.prototype.addEventListener;
  window.removeEventListener = FakeEventTarget.prototype.removeEventListener;

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
  const zoomOut = new FakeElement("button", ["[data-image-viewer-zoom-out]"]);
  const zoomValue = new FakeElement("output", ["[data-image-viewer-zoom-value]"]);
  const zoomIn = new FakeElement("button", ["[data-image-viewer-zoom-in]"]);
  const actualSize = new FakeElement("button", ["[data-image-viewer-actual-size]"]);
  const fit = new FakeElement("button", ["[data-image-viewer-fit]"]);
  const download = new FakeElement("a", ["[data-image-viewer-download]"]);
  stage.append(image);
  overlay.append(stage, close, zoomOut, zoomValue, zoomIn, actualSize, fit, download);
  document.body.append(overlay);

  const controller = new ImageViewerController(document, window);
  controller.bind();
  return { controller, document, overlay, stage, image, close, zoomValue, download };
}

function sourceImage() {
  const article = new FakeElement("article");
  const container = new FakeElement("div");
  const button = new FakeElement("button", ["[data-image-viewer-open]"]);
  const image = new FakeElement("img");
  image.src = "/attachments/image.png";
  image.currentSrc = image.src;
  image.alt = "Attached image";
  image.naturalWidth = 1600;
  image.naturalHeight = 1200;
  button.append(image);
  container.append(button);
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
  const { controller, document, overlay, close, zoomValue, download } = viewerFixture();
  const { article, container, button, image } = sourceImage();
  document.body.append(article);

  controller.open(image, button);

  assert.equal(controller.isOpen, true);
  assert.equal(overlay.hidden, false);
  assert.equal(controller.scale, 0.42);
  assert.equal(zoomValue.textContent, "42%");
  assert.equal(download.getAttribute("href"), "/attachments/image.png");
  assert.equal(download.getAttribute("download"), "image.png");
  assert.equal(close.focused, true);

  close.focused = false;
  document.activeElement = download;
  let trapped = false;
  controller.trapFocus({ shiftKey: false, preventDefault() { trapped = true; } });
  assert.equal(trapped, true);
  assert.equal(close.focused, true);

  controller.showActualSize();
  assert.equal(controller.scale, 1);
  assert.equal(zoomValue.textContent, "100%");

  const replacement = new FakeElement("button", ["[data-image-viewer-open]"]);
  container.remove();
  article.append(replacement);
  button.isConnected = false;
  controller.close();
  assert.equal(controller.isOpen, false);
  assert.equal(overlay.hidden, true);
  assert.equal(download.getAttribute("href"), null);
  assert.equal(download.getAttribute("download"), null);
  assert.equal(button.focused, undefined);
  assert.equal(replacement.focused, true);
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

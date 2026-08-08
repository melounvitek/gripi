const VIEWER_PADDING = 16;
const VIEWER_CONTROLS_SPACE = 96;
const MAX_SCALE = 4;
const ZOOM_STEP = 1.25;

function distance(first, second) {
  return Math.hypot(second.x - first.x, second.y - first.y);
}

function midpoint(first, second) {
  return { x: (first.x + second.x) / 2, y: (first.y + second.y) / 2 };
}

function clamp(value, minimum, maximum) {
  return Math.min(maximum, Math.max(minimum, value));
}

export class ImageViewerController {
  constructor(document, window) {
    this.document = document;
    this.window = window;
    this.isOpen = false;
    this.scale = 1;
    this.fitScale = 1;
    this.offsetX = 0;
    this.offsetY = 0;
    this.naturalWidth = 0;
    this.naturalHeight = 0;
    this.pointers = new Map();
    this.panStart = null;
    this.pinchStart = null;
    this.suppressClick = false;
    this.pointerStartedOnBackdrop = null;
    this.bound = false;
  }

  bind() {
    if (this.bound) return;

    this.viewer = this.document.querySelector("[data-image-viewer]");
    this.stage = this.viewer?.querySelector("[data-image-viewer-stage]");
    this.image = this.viewer?.querySelector("[data-image-viewer-image]");
    this.closeButton = this.viewer?.querySelector("[data-image-viewer-close]");
    this.zoomOutButton = this.viewer?.querySelector("[data-image-viewer-zoom-out]");
    this.zoomValue = this.viewer?.querySelector("[data-image-viewer-zoom-value]");
    this.zoomInButton = this.viewer?.querySelector("[data-image-viewer-zoom-in]");
    this.actualSizeButton = this.viewer?.querySelector("[data-image-viewer-actual-size]");
    this.fitButton = this.viewer?.querySelector("[data-image-viewer-fit]");
    if (!this.viewer || !this.stage || !this.image) return;

    this.document.addEventListener("click", (event) => this.handleDocumentClick(event));
    this.document.addEventListener("keydown", (event) => this.handleKeyDown(event));
    this.window.addEventListener("resize", () => this.handleResize());
    this.image.addEventListener("load", () => this.initializeImage());
    this.image.addEventListener("error", () => this.handleImageError());
    this.stage.addEventListener("click", (event) => this.handleViewerClick(event));
    this.stage.addEventListener("dblclick", (event) => this.handleDoubleClick(event));
    this.stage.addEventListener("wheel", (event) => this.handleWheel(event), { passive: false });
    this.stage.addEventListener("pointerdown", (event) => this.handlePointerDown(event));
    this.stage.addEventListener("pointermove", (event) => this.handlePointerMove(event));
    this.stage.addEventListener("pointerup", (event) => this.handlePointerUp(event));
    this.stage.addEventListener("pointercancel", (event) => this.handlePointerUp(event));
    this.closeButton?.addEventListener("click", () => this.close());
    this.zoomOutButton?.addEventListener("click", () => this.zoomBy(1 / ZOOM_STEP));
    this.zoomInButton?.addEventListener("click", () => this.zoomBy(ZOOM_STEP));
    this.actualSizeButton?.addEventListener("click", () => this.showActualSize());
    this.fitButton?.addEventListener("click", () => this.showFit());
    this.bound = true;
  }

  handleDocumentClick(event) {
    const opener = event.target.closest?.("[data-image-viewer-open]");
    const source = opener?.querySelector("img");
    if (!source) return;

    event.preventDefault();
    this.open(source, opener);
  }

  open(source, opener = null) {
    if (!this.viewer || !this.image || !source) return false;

    this.opener = opener || this.document.activeElement;
    this.isOpen = true;
    this.viewer.hidden = false;
    this.viewer.dataset.loading = "true";
    delete this.viewer.dataset.loadError;
    this.image.alt = source.alt || "Full-size image";
    this.image.src = source.currentSrc || source.src;
    this.closeButton?.focus({ preventScroll: true });

    if (this.image.complete && this.image.naturalWidth > 0) this.initializeImage();
    return true;
  }

  close() {
    if (!this.isOpen) return;

    const opener = this.opener;
    this.isOpen = false;
    this.viewer.hidden = true;
    this.pointers.clear();
    this.pointerStartedOnBackdrop = null;
    this.panStart = null;
    this.pinchStart = null;
    this.image.removeAttribute("src");
    this.opener = null;
    opener?.focus({ preventScroll: true });
  }

  initializeImage() {
    if (!this.isOpen || this.image.naturalWidth <= 0 || this.image.naturalHeight <= 0) return;

    this.naturalWidth = this.image.naturalWidth;
    this.naturalHeight = this.image.naturalHeight;
    delete this.viewer.dataset.loading;
    delete this.viewer.dataset.loadError;
    this.showFit();
  }

  handleImageError() {
    if (!this.isOpen) return;

    delete this.viewer.dataset.loading;
    this.viewer.dataset.loadError = "true";
  }

  calculateFitScale() {
    if (!this.naturalWidth || !this.naturalHeight) return 1;

    const availableWidth = Math.max(1, this.stage.clientWidth - VIEWER_PADDING * 2);
    const availableHeight = Math.max(1, this.stage.clientHeight - VIEWER_CONTROLS_SPACE);
    return Math.min(1, availableWidth / this.naturalWidth, availableHeight / this.naturalHeight);
  }

  showFit() {
    this.fitScale = this.calculateFitScale();
    this.scale = this.fitScale;
    this.resetPosition();
  }

  showActualSize() {
    this.setScale(1, this.stageCenter());
  }

  resetPosition() {
    this.offsetX = 0;
    this.offsetY = 0;
    this.applyTransform();
  }

  stageCenter() {
    return { x: this.stage.clientWidth / 2, y: this.stage.clientHeight / 2 };
  }

  stagePoint(clientX, clientY) {
    const bounds = this.stage.getBoundingClientRect();
    return { x: clientX - bounds.left, y: clientY - bounds.top };
  }

  zoomBy(factor, point = this.stageCenter()) {
    this.setScale(this.scale * factor, point);
  }

  setScale(scale, point = this.stageCenter()) {
    const nextScale = clamp(scale, this.fitScale, MAX_SCALE);
    const center = this.stageCenter();
    const ratio = nextScale / this.scale;
    this.offsetX = point.x - center.x - (point.x - center.x - this.offsetX) * ratio;
    this.offsetY = point.y - center.y - (point.y - center.y - this.offsetY) * ratio;
    this.scale = nextScale;
    this.clampOffsets();
    this.applyTransform();
  }

  clampOffsets() {
    const maximumX = Math.max(0, (this.naturalWidth * this.scale - this.stage.clientWidth) / 2);
    const maximumY = Math.max(0, (this.naturalHeight * this.scale - this.stage.clientHeight) / 2);
    this.offsetX = clamp(this.offsetX, -maximumX, maximumX);
    this.offsetY = clamp(this.offsetY, -maximumY, maximumY);
  }

  applyTransform() {
    if (!this.image) return;

    this.image.style.transform = `translate(calc(-50% + ${this.offsetX}px), calc(-50% + ${this.offsetY}px)) scale(${this.scale})`;
    if (this.zoomValue) this.zoomValue.textContent = `${Math.round(this.scale * 100)}%`;
    if (this.zoomOutButton) this.zoomOutButton.disabled = this.scale <= this.fitScale + 0.0001;
    if (this.zoomInButton) this.zoomInButton.disabled = this.scale >= MAX_SCALE - 0.0001;
  }

  handleWheel(event) {
    if (!this.isOpen) return;

    event.preventDefault();
    const factor = Math.exp(-event.deltaY * 0.002);
    this.zoomBy(factor, this.stagePoint(event.clientX, event.clientY));
  }

  handlePointerDown(event) {
    if (!this.isOpen) return;

    event.preventDefault();
    if (this.pointers.size === 0) this.pointerStartedOnBackdrop = event.target === this.stage;
    this.stage.setPointerCapture?.(event.pointerId);
    this.pointers.set(event.pointerId, this.stagePoint(event.clientX, event.clientY));
    this.suppressClick = false;
    this.startPointerGesture();
  }

  startPointerGesture() {
    const points = [...this.pointers.values()];
    if (points.length === 1) {
      this.panStart = { point: points[0], x: this.offsetX, y: this.offsetY };
      this.pinchStart = null;
    } else if (points.length >= 2) {
      const center = this.stageCenter();
      const middle = midpoint(points[0], points[1]);
      this.pinchStart = {
        distance: distance(points[0], points[1]),
        scale: this.scale,
        localX: (middle.x - center.x - this.offsetX) / this.scale,
        localY: (middle.y - center.y - this.offsetY) / this.scale,
      };
      this.panStart = null;
    }
  }

  handlePointerMove(event) {
    if (!this.isOpen || !this.pointers.has(event.pointerId)) return;

    event.preventDefault();
    this.pointers.set(event.pointerId, this.stagePoint(event.clientX, event.clientY));
    const points = [...this.pointers.values()];
    if (points.length >= 2 && this.pinchStart) {
      const currentDistance = distance(points[0], points[1]);
      const nextScale = clamp(this.pinchStart.scale * currentDistance / Math.max(1, this.pinchStart.distance), this.fitScale, MAX_SCALE);
      const middle = midpoint(points[0], points[1]);
      const center = this.stageCenter();
      this.scale = nextScale;
      this.offsetX = middle.x - center.x - this.pinchStart.localX * nextScale;
      this.offsetY = middle.y - center.y - this.pinchStart.localY * nextScale;
      this.clampOffsets();
      this.applyTransform();
      this.suppressClick = true;
      return;
    }
    if (points.length !== 1 || !this.panStart) return;

    const movementX = points[0].x - this.panStart.point.x;
    const movementY = points[0].y - this.panStart.point.y;
    this.offsetX = this.panStart.x + movementX;
    this.offsetY = this.panStart.y + movementY;
    this.clampOffsets();
    this.applyTransform();
    if (Math.hypot(movementX, movementY) > 3) this.suppressClick = true;
  }

  handlePointerUp(event) {
    if (!this.pointers.has(event.pointerId)) return;

    this.stage.releasePointerCapture?.(event.pointerId);
    this.pointers.delete(event.pointerId);
    this.startPointerGesture();
  }

  handleDoubleClick(event) {
    if (!this.isOpen) return;

    event.preventDefault();
    if (Math.abs(this.scale - this.fitScale) < 0.001) this.showActualSize();
    else this.showFit();
  }

  handleViewerClick(event) {
    if (!this.isOpen) return;

    const startedOnBackdrop = this.pointerStartedOnBackdrop;
    this.pointerStartedOnBackdrop = null;
    if (startedOnBackdrop === false || event.target !== this.stage) {
      this.suppressClick = false;
      return;
    }
    if (this.suppressClick) {
      this.suppressClick = false;
      return;
    }

    event.preventDefault();
    this.close();
  }

  handleResize() {
    if (!this.isOpen || !this.naturalWidth) return;

    const wasFitted = Math.abs(this.scale - this.fitScale) < 0.001;
    this.fitScale = this.calculateFitScale();
    if (wasFitted) {
      this.scale = this.fitScale;
      this.resetPosition();
    } else {
      this.scale = clamp(this.scale, this.fitScale, MAX_SCALE);
      this.clampOffsets();
      this.applyTransform();
    }
  }

  handleKeyDown(event) {
    if (!this.isOpen) return;

    if (event.key === "Escape") {
      event.preventDefault();
      event.stopImmediatePropagation?.();
      this.close();
    } else if (["+", "="].includes(event.key)) {
      event.preventDefault();
      this.zoomBy(ZOOM_STEP);
    } else if (event.key === "-") {
      event.preventDefault();
      this.zoomBy(1 / ZOOM_STEP);
    } else if (event.key === "0") {
      event.preventDefault();
      this.showActualSize();
    } else if (event.key.toLocaleLowerCase() === "f") {
      event.preventDefault();
      this.showFit();
    } else if (event.key === "Tab") {
      this.trapFocus(event);
    }
  }

  trapFocus(event) {
    const controls = [this.closeButton, this.zoomOutButton, this.zoomInButton, this.actualSizeButton, this.fitButton]
      .filter((control) => control && !control.disabled);
    if (controls.length === 0) return;

    const current = controls.indexOf(this.document.activeElement);
    const next = event.shiftKey ? (current <= 0 ? controls.length - 1 : current - 1) : (current === controls.length - 1 ? 0 : current + 1);
    if (current === -1 || event.shiftKey && current === 0 || !event.shiftKey && current === controls.length - 1) {
      event.preventDefault();
      controls[next].focus();
    }
  }
}

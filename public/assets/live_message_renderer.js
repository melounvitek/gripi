import { PAIRED_TOOL_NAMES, TOOL_OUTPUT_DESKTOP_TAIL_LINES, TOOL_OUTPUT_MOBILE_TAIL_LINES } from "./constants.js";
import { eventTimestamp, formatTimestamp, messageFingerprint, messageRoleKey, messageRoleLabel, messageTimestampKey, normalizedMessageText, stableTextHash } from "./formatting.js";

export class LiveMessageRenderer {
  constructor(document, conversationController, parser, markdownRenderer) {
    this.document = document;
    this.conversationController = conversationController;
    this.parser = parser;
    this.markdownRenderer = markdownRenderer;
    this.liveOutput = null;
    this.conversationScroll = null;
    this.liveCompactionRendered = false;
    this.resetLiveAssistantTracking();
  }

  bind() {
    this.markdownRenderer.bind();
    this.liveOutput = this.document.getElementById("live-output");
    this.conversationScroll = this.conversationController.element;
    this.liveCompactionRendered = false;
    this.resetLiveAssistantTracking();
  }

  liveMessageAlreadyRendered(roleName, text, timestampKey) {
    const fingerprint = messageFingerprint(roleName, text, timestampKey);
    if (!fingerprint) return false;
    return [...(this.conversationScroll?.querySelectorAll(".message:not(.message--live)[data-message-fingerprint]") || [])].some((message) => message.dataset.messageFingerprint === fingerprint);
  }

  optimisticUserMessage(text) {
    const targetText = normalizedMessageText(text);
    return [...(this.conversationScroll?.querySelectorAll('.message--live[data-role="user"][data-optimistic="true"]') || [])]
      .find((message) => {
        const optimisticText = normalizedMessageText(message.hasAttribute("data-optimistic-text") ? message.dataset.optimisticText : message.querySelector(".message-body")?.textContent);
        if (optimisticText === targetText) return true;
        if (Number(message.dataset.optimisticImageCount || 0) <= 0) return false;
        if (!optimisticText) return targetText.length > 0;
        return targetText.startsWith(`${optimisticText}\n`);
      });
  }

  optimisticUserMessageAlreadyRendered(text) {
    return !!this.optimisticUserMessage(text);
  }

  markOptimisticUserMessageFailed(text) {
    const message = this.optimisticUserMessage(text);
    if (!message) return;
    delete message.dataset.optimistic;
    delete message.dataset.optimisticText;
    delete message.dataset.optimisticImageCount;
  }

  replaceMessageImages(article, images = []) {
    article.querySelector(".message-images")?.remove();
    this.renderMessageImages(article, images);
  }

  renderMessageImages(article, images = []) {
    const visibleImages = images.filter((image) => image?.src);
    if (visibleImages.length === 0) return;

    const container = this.document.createElement("div");
    container.className = "message-images";
    visibleImages.forEach((image) => {
      const element = this.document.createElement("img");
      element.className = "message-image";
      element.src = image.src;
      element.alt = image.alt || "Attached image";
      element.loading = "lazy";
      element.decoding = "async";
      if (image.src.startsWith("blob:")) {
        const revoke = () => URL.revokeObjectURL(image.src);
        element.addEventListener("load", revoke, { once: true });
        element.addEventListener("error", revoke, { once: true });
      }
      container.append(element);
    });
    article.append(container);
  }

  appendMessage(roleName, text, live = true, forceScroll = false, timestamp = null, options = {}) {
    const timestampKey = messageTimestampKey(timestamp);
    if (live && roleName === "user" && !options.optimistic && this.optimisticUserMessageAlreadyRendered(text)) return null;
    if (live && this.liveMessageAlreadyRendered(roleName, text, timestampKey)) return null;

    const shouldScroll = this.conversationController.followLiveOutput(forceScroll);
    const roleKey = messageRoleKey(roleName);

    const article = this.document.createElement("article");
    article.className = `message message--${roleKey}${options.thinking ? " message--thinking" : ""}${live ? " message--live" : ""}`;
    article.dataset.role = roleName;
    article.dataset.messageTimestamp = timestampKey;
    article.dataset.messageFingerprint = messageFingerprint(roleName, text, timestampKey);
    if (options.finalAssistantResponse) article.dataset.finalAssistantResponse = "true";
    if (options.optimistic) {
      article.dataset.optimistic = "true";
      article.dataset.optimisticText = options.optimisticText ?? text;
      article.dataset.optimisticImageCount = String(options.images?.length || 0);
    }

    const header = this.document.createElement("header");
    header.className = "message-header";

    const role = this.document.createElement("div");
    role.className = "role";
    role.textContent = messageRoleLabel(roleName);

    const body = this.document.createElement(roleName === "assistant" ? "div" : "pre");
    body.className = options.thinking ? "message-body message-body--thinking message-body--markdown" : (roleName === "assistant" ? "message-body message-body--markdown" : "message-body");
    if (roleName === "assistant") {
      this.markdownRenderer.render(body, text);
    } else {
      body.textContent = text;
    }

    const meta = this.document.createElement("div");
    meta.className = "message-meta";
    meta.textContent = formatTimestamp(timestamp);

    header.append(role);
    if (meta.textContent) header.append(meta);
    if (roleName === "assistant" && !options.thinking) header.append(this.makeCopyButton());
    article.append(header);
    article.append(body);
    this.renderMessageImages(article, options.images);
    this.liveOutput.append(article);
    this.conversationController.afterLiveOutputChange(shouldScroll, live);
    return { article, body, compact: false };
  }

  appendCompactMessage(roleName, summary, text, live = true, forceScroll = false, timestamp = null, options = {}) {
    const timestampKey = messageTimestampKey(timestamp);
    if (live && this.liveMessageAlreadyRendered(roleName, text, timestampKey)) return null;

    const shouldScroll = this.conversationController.followLiveOutput(forceScroll);
    const roleKey = messageRoleKey(roleName);

    const article = this.document.createElement("article");
    article.className = `message message--${roleKey} message--compact${options.toolName && roleName === "assistant" ? " message--tool-call" : ""}${options.toolTranscript ? " message--tool-transcript" : ""}${options.error === true ? " message--tool-error" : ""}${live ? " message--live" : ""}`;
    article.dataset.role = roleName;
    article.dataset.messageTimestamp = timestampKey;
    article.dataset.messageFingerprint = messageFingerprint(roleName, text, timestampKey);

    const header = this.document.createElement("header");
    header.className = "message-header";
    const role = this.document.createElement("div");
    role.className = "role";
    role.textContent = options.toolName && roleName === "assistant" ? "tool" : messageRoleLabel(roleName);
    const meta = this.document.createElement("div");
    meta.className = "message-meta";
    meta.textContent = formatTimestamp(timestamp, options.timestampFallback !== false);
    header.append(role);
    if (meta.textContent) header.append(meta);

    const details = this.document.createElement("div");
    details.className = "message-details message-details--always-open";
    const summaryElement = this.document.createElement("div");
    summaryElement.className = "message-details-summary";
    const summaryText = this.document.createElement("span");
    summaryText.className = "compact-summary";
    this.renderToolSummary(summaryText, options.summaryParts, summary);
    summaryElement.append(summaryText);
    const output = this.document.createElement("div");
    output.className = "tool-output-collapse";
    output.dataset.toolOutputCollapse = "";
    output.dataset.toolOutputCollapsible = ["assistant", "tool", "toolResult"].includes(roleName) ? "true" : "false";
    output.dataset.collapsed = "false";
    const control = this.document.createElement("div");
    control.className = "tool-output-collapse-control";
    control.dataset.toolOutputCollapseControl = "";
    const desktopCount = this.document.createElement("span");
    desktopCount.className = "tool-output-hidden-count tool-output-hidden-count--desktop";
    const mobileCount = this.document.createElement("span");
    mobileCount.className = "tool-output-hidden-count tool-output-hidden-count--mobile";
    const toggle = this.document.createElement("button");
    toggle.type = "button";
    toggle.className = "tool-output-toggle";
    toggle.dataset.toolOutputToggle = "";
    toggle.setAttribute("aria-expanded", "false");
    toggle.textContent = "Expand";
    control.append(desktopCount, mobileCount, toggle);
    const body = this.document.createElement("pre");
    body.className = "message-body";
    body.dataset.toolOutputBody = "";
    const fullTemplate = this.document.createElement("template");
    fullTemplate.dataset.toolOutputFull = "";
    const tailTemplate = this.document.createElement("template");
    tailTemplate.dataset.toolOutputTail = "";
    output.append(control, body, fullTemplate, tailTemplate);
    this.renderToolTranscriptBody(body, text, options.toolName, { preview: options.toolPreview === true });
    details.append(summaryElement, output);

    const entry = { article, details, output, body, summaryText, compact: true, toolName: options.toolName || "" };
    this.renderSubagentPrompt(entry, options.toolPrompt);
    article.append(header, details);
    this.renderMessageImages(article, options.images);
    this.liveOutput.append(article);
    this.conversationController.afterLiveOutputChange(shouldScroll, live);
    return entry;
  }

  renderSubagentPrompt(entry, prompt) {
    if (typeof prompt !== "string" || !prompt.trim() || !entry?.details || entry.subagentPromptElement) return;
    entry.subagentPrompt = prompt;
    const details = this.document.createElement("details");
    details.className = "subagent-prompt";
    details.dataset.subagentPrompt = "";
    const summary = this.document.createElement("summary");
    summary.className = "subagent-prompt-summary";
    const label = this.document.createElement("span");
    label.className = "subagent-prompt-label";
    label.textContent = "Prompt";
    const preview = this.document.createElement("span");
    preview.className = "subagent-prompt-preview";
    preview.dataset.subagentPromptPreview = "";
    preview.textContent = prompt;
    const action = this.document.createElement("span");
    action.className = "subagent-prompt-action";
    action.setAttribute("aria-hidden", "true");
    summary.append(label, preview, action);
    details.append(summary);
    entry.details.insertBefore(details, entry.output || null);
    entry.subagentPromptElement = details;
    entry.subagentPromptPreview = preview;
  }

  renderToolTranscriptBody(body, text, toolName = "", options = {}) {
    const preview = options.preview === true && toolName === "edit";
    body.dataset.rawText = text || "";
    const rawText = body.dataset.rawText;
    const lines = String(rawText).split("\n");
    if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
    const collapse = body.closest("[data-tool-output-collapse]");
    body.classList.toggle("message-body--edit-preview", preview);

    if (!collapse) {
      body.replaceChildren(this.toolOutputContentNode(lines, toolName, preview, 0));
      return;
    }

    const hasText = rawText !== "";
    collapse.hidden = !hasText;
    if (!hasText) {
      const control = collapse.querySelector("[data-tool-output-collapse-control]");
      if (control) control.hidden = true;
      body.replaceChildren();
      return;
    }

    const expanded = collapse.dataset.expanded === "true";
    const shouldCollapse = collapse.dataset.toolOutputCollapsible === "true" && lines.length > TOOL_OUTPUT_DESKTOP_TAIL_LINES;
    const fullTemplate = collapse.querySelector("[data-tool-output-full]");
    const tailTemplate = collapse.querySelector("[data-tool-output-tail]");
    const control = collapse.querySelector("[data-tool-output-collapse-control]");
    const desktopCount = collapse.querySelector(".tool-output-hidden-count--desktop");
    const mobileCount = collapse.querySelector(".tool-output-hidden-count--mobile");

    fullTemplate?.content.replaceChildren(this.toolOutputContentNode(lines, toolName, preview, 0));
    if (shouldCollapse) {
      const tailLines = lines.slice(-TOOL_OUTPUT_DESKTOP_TAIL_LINES);
      const desktopExtraCount = Math.max(tailLines.length - TOOL_OUTPUT_MOBILE_TAIL_LINES, 0);
      tailTemplate?.content.replaceChildren(this.toolOutputContentNode(tailLines, toolName, preview, desktopExtraCount));
      if (desktopCount) desktopCount.textContent = `… (${Math.max(lines.length - TOOL_OUTPUT_DESKTOP_TAIL_LINES, 0)} earlier lines)`;
      if (mobileCount) mobileCount.textContent = `… (${Math.max(lines.length - TOOL_OUTPUT_MOBILE_TAIL_LINES, 0)} earlier lines)`;
    }

    control.hidden = !shouldCollapse;
    collapse.dataset.collapsed = shouldCollapse && !expanded ? "true" : "false";
    body.replaceChildren(...Array.from((shouldCollapse && !expanded ? tailTemplate : fullTemplate).content.cloneNode(true).childNodes));
  }

  toolOutputContentNode(lines, toolName, preview, desktopOnlyCount) {
    const content = this.document.createElement("span");
    content.className = "tool-output-content";
    content.append(...this.toolOutputLineNodes(lines, toolName, preview, desktopOnlyCount));
    return content;
  }

  toolOutputLineNodes(lines, toolName, preview, desktopOnlyCount) {
    if (["edit", "write"].includes(toolName)) {
      return lines.map((line, index) => {
        const span = this.document.createElement("span");
        span.className = `tool-diff-line ${this.toolDiffLineClass(line, preview)}`;
        if (index < desktopOnlyCount) span.classList.add("tool-output-tail-desktop-extra");
        span.textContent = preview ? line : this.parser.displayHomePath(line);
        return span;
      });
    }

    return lines.map((line, index) => {
      const span = this.document.createElement("span");
      span.className = `tool-output-line${index < desktopOnlyCount ? " tool-output-tail-desktop-extra" : ""}`;
      span.textContent = this.parser.displayHomePath(line);
      return span;
    });
  }

  toolDiffLineClass(line, preview = false) {
    if (preview && /^Edit \d+/.test(line)) return "tool-diff-line--meta tool-diff-line--preview-heading";
    if (line.startsWith("+")) return `tool-diff-line--add${preview && line === "+ …" ? " tool-diff-line--preview-ellipsis" : ""}`;
    if (line.startsWith("-")) return `tool-diff-line--remove${preview && line === "- …" ? " tool-diff-line--preview-ellipsis" : ""}`;
    if (/^(Edit \d+|write\b|Wrote\b)/.test(line)) return "tool-diff-line--meta";
    return "tool-diff-line--context";
  }

  renderToolSummary(container, parts, fallback) {
    container.replaceChildren();
    if (!parts) {
      container.textContent = fallback;
      return;
    }

    const command = this.document.createElement("span");
    command.className = "tool-command";
    command.textContent = parts.name;
    container.append(command);
    if (parts.path) {
      container.append(" ");
      const path = this.document.createElement("span");
      path.className = "tool-path";
      path.textContent = parts.path;
      container.append(path);
    }
    if (parts.range) {
      const range = this.document.createElement("span");
      range.className = "tool-range";
      range.textContent = `:${parts.range}`;
      container.append(range);
    }
  }

  makeCopyButton() {
    const button = this.document.createElement("button");
    button.type = "button";
    button.className = "copy-button";
    button.dataset.copyTarget = "message";
    button.textContent = "Copy";
    return button;
  }


  resetLiveAssistantTracking() {
    this.liveAssistantSegments = new Map();
    this.livePairedToolCalls = new Map();
    this.liveToolExecutions = new Map();
    this.liveUserMessages = new Map();
    this.liveAssistantSeen = false;
  }

  resetLiveCompactionTracking() {
    this.liveCompactionRendered = false;
  }

  segmentIdentity(event, segment, fallbackIndex) {
    const update = event.assistantMessageEvent || {};
    if (segment.toolCallId) return `${segment.toolCallId}-${segment.compact ? "compact" : "text"}`;
    const contentIndex = segment.startIndex ?? update.contentIndex ?? fallbackIndex;
    return `${contentIndex}-${segment.compact ? "compact" : "text"}`;
  }

  clearLiveAssistantStreaming() {
    this.conversationScroll?.querySelectorAll(".message--assistant.message--streaming").forEach((article) => article.classList.remove("message--streaming"));
    this.liveAssistantSegments.forEach((entry) => entry.article.classList.remove("message--streaming"));
  }

  forgetLiveEntry(entry) {
    this.liveAssistantSegments.forEach((storedEntry, key) => {
      if (storedEntry === entry) this.liveAssistantSegments.delete(key);
    });
    this.livePairedToolCalls.forEach((storedEntry, key) => {
      if (storedEntry === entry) this.livePairedToolCalls.delete(key);
    });
    this.liveToolExecutions.forEach((storedEntry, key) => {
      if (storedEntry === entry) this.liveToolExecutions.delete(key);
    });
    this.liveUserMessages.forEach((storedEntry, key) => {
      if (storedEntry === entry) this.liveUserMessages.delete(key);
    });
  }

  markLiveEntryRendered(entry, roleName, text, timestamp = null) {
    const timestampKey = messageTimestampKey(timestamp) || entry.article.dataset.messageTimestamp;
    if (this.liveMessageAlreadyRendered(roleName, text, timestampKey)) {
      entry.article.remove();
      this.forgetLiveEntry(entry);
      return false;
    }
    entry.article.dataset.messageTimestamp = timestampKey;
    entry.article.dataset.messageFingerprint = messageFingerprint(roleName, text, timestampKey);
    return true;
  }

  updateLiveSegment(entry, roleName, segment, shouldScroll, timestamp = null) {
    if (entry.compact !== segment.compact) return null;
    const displayText = roleName === "user" && entry.userDisplayText ? entry.userDisplayText : segment.text;

    if (segment.compact) {
      this.renderToolSummary(entry.summaryText, segment.summaryParts, segment.summary);
      this.renderToolTranscriptBody(entry.body, segment.text, segment.toolName || entry.toolName, { preview: segment.toolPreview === true });
    } else {
      if (roleName === "assistant") {
        this.markdownRenderer.render(entry.body, segment.text);
      } else {
        entry.body.textContent = displayText;
      }
    }

    if (!this.markLiveEntryRendered(entry, roleName, segment.text, timestamp)) return null;
    this.replaceMessageImages(entry.article, segment.images);
    this.conversationController.afterLiveOutputChange(shouldScroll);
    return entry;
  }

  liveUserIdentity(event, segment, fallbackIndex, timestamp) {
    const message = this.parser.eventMessage(event);
    const id = message?.id || message?.messageId || event.id || event.messageId;
    const index = segment.startIndex ?? fallbackIndex;
    if (id) return `${id}-${index}`;

    const timestampKey = messageTimestampKey(timestamp);
    const textHash = stableTextHash(normalizedMessageText(segment.text));
    return `${timestampKey || "untimed"}-${textHash}-${index}`;
  }

  optimisticUserEntry(segment, timestamp) {
    const article = this.optimisticUserMessage(segment.text);
    if (!article) return null;

    delete article.dataset.optimistic;
    delete article.dataset.optimisticText;
    delete article.dataset.optimisticImageCount;
    article.dataset.messageTimestamp = messageTimestampKey(timestamp);
    article.dataset.messageFingerprint = messageFingerprint("user", segment.text, article.dataset.messageTimestamp);
    const meta = article.querySelector(".message-meta");
    if (meta) meta.textContent = formatTimestamp(timestamp);
    const body = article.querySelector(".message-body");
    const entry = { article, body, compact: false, userDisplayText: body?.textContent || segment.text };
    return entry;
  }

  upsertLiveUserSegment(event, segment, fallbackIndex, shouldScroll, timestamp) {
    const key = this.liveUserIdentity(event, segment, fallbackIndex, timestamp);
    const existing = this.liveUserMessages.get(key);
    if (existing) return this.updateLiveSegment(existing, "user", segment, shouldScroll, timestamp);

    const optimisticEntry = this.optimisticUserEntry(segment, timestamp);
    const entry = optimisticEntry || this.appendMessage("user", segment.text, true, shouldScroll, timestamp, { images: segment.images });
    if (optimisticEntry) this.replaceMessageImages(entry.article, segment.images);
    if (!entry) return null;
    this.liveUserMessages.set(key, entry);
    return entry;
  }

  upsertLiveAssistantSegment(event, roleName, segment, fallbackIndex, shouldScroll, timestamp) {
    const key = this.segmentIdentity(event, segment, fallbackIndex);
    const finalAssistantResponse = event.type === "message_end" && segment.finalAssistantResponse;
    const streamingAssistantResponse = event.type !== "message_end" && !segment.compact && !segment.thinking;
    const existing = this.liveAssistantSegments.get(key);
    if (existing) {
      const updated = this.updateLiveSegment(existing, roleName, segment, shouldScroll, timestamp);
      if (updated) {
        updated.article.classList.toggle("message--streaming", streamingAssistantResponse);
        if (finalAssistantResponse) updated.article.dataset.finalAssistantResponse = "true";
        return updated;
      }
      this.forgetLiveEntry(existing);
      if (this.liveMessageAlreadyRendered(roleName, segment.text, messageTimestampKey(timestamp))) return null;
    }
    if (this.liveMessageAlreadyRendered(roleName, segment.text, messageTimestampKey(timestamp))) return null;

    const entry = segment.compact ?
      this.appendCompactMessage(roleName, segment.summary, segment.text, true, shouldScroll, timestamp, { summaryParts: segment.summaryParts, toolTranscript: segment.toolTranscript, toolName: segment.toolName, toolPreview: segment.toolPreview, toolPrompt: segment.toolPrompt, error: segment.error, images: segment.images }) :
      this.appendMessage("assistant", segment.text, true, shouldScroll, timestamp, { thinking: segment.thinking, finalAssistantResponse, images: segment.images });
    if (!entry) return null;
    entry.article.classList.toggle("message--streaming", streamingAssistantResponse);
    if (finalAssistantResponse) entry.article.dataset.finalAssistantResponse = "true";
    this.liveAssistantSegments.set(key, entry);
    if (PAIRED_TOOL_NAMES.has(segment.toolName) && segment.toolCallId && !segment.isToolResult) this.livePairedToolCalls.set(segment.toolCallId, entry);
    if (segment.toolCallId && !segment.isToolResult && !PAIRED_TOOL_NAMES.has(segment.toolName)) this.liveToolExecutions.set(segment.toolCallId, entry);
    return entry;
  }


  retainSubagentDetails(entry, details, finalStatus = null) {
    entry.subagentDetails = this.parser.retainedSubagentDetails(entry.subagentDetails, details, finalStatus);
    return entry.subagentDetails;
  }

  updateLiveToolExecution(entry, event, shouldScroll) {
    if (event.toolName === "subagent") {
      this.renderSubagentPrompt(entry, this.parser.subagentPromptFromEvent(event));
      const finalStatus = event.type === "tool_execution_end" ? (event.isError ? "error" : "done") : null;
      const eventDetails = this.parser.subagentDetailsFromEvent(event);
      const freshDetails = this.parser.richSubagentDetails(eventDetails);
      const details = this.retainSubagentDetails(entry, eventDetails, finalStatus);
      const fallback = this.parser.toolExecutionContentText(event) || (event.type === "tool_execution_end" ? "(done)" : "(running…)");
      this.renderToolSummary(entry.summaryText, null, details ? this.parser.subagentSummary(details, this.parser.subagentRunning(event)) : this.parser.toolExecutionSummary(event));
      this.renderToolTranscriptBody(entry.body, details ? this.parser.subagentDisplayText(details, fallback, this.parser.subagentRunning(event), !freshDetails) : fallback, event.toolName);
    } else {
      this.renderToolSummary(entry.summaryText, null, this.parser.toolExecutionSummary(event));
      this.renderToolTranscriptBody(entry.body, this.parser.toolExecutionText(event), event.toolName || entry.toolName);
    }
    entry.article.classList.toggle("message--tool-error", event.isError === true);
    this.conversationController.afterLiveOutputChange(shouldScroll);
    return entry;
  }

  renderToolExecutionEvent(event, timestamp = eventTimestamp(event), timestampFallback = true, restoredPrompt = "") {
    if (!event.toolCallId || PAIRED_TOOL_NAMES.has(event.toolName)) return;
    const shouldScroll = this.conversationController.followLiveOutput();
    const existing = this.liveToolExecutions.get(event.toolCallId);
    if (existing) {
      this.updateLiveToolExecution(existing, event, shouldScroll);
      return;
    }

    const entry = this.appendCompactMessage("tool", this.parser.toolExecutionSummary(event), this.parser.toolExecutionText(event), true, shouldScroll, timestamp, { toolName: event.toolName, toolPrompt: event.toolName === "subagent" ? this.parser.subagentPromptFromEvent(event, restoredPrompt) : "", error: event.isError === true, timestampFallback });
    if (entry) {
      if (event.toolName === "subagent") this.retainSubagentDetails(entry, this.parser.subagentDetailsFromEvent(event));
      this.liveToolExecutions.set(event.toolCallId, entry);
    }
  }

  restoreActiveToolExecutions() {
    if (!this.liveOutput?.dataset.activeToolEvents) return;

    const serializedEvents = this.liveOutput.dataset.activeToolEvents;
    const serializedTimestamps = this.liveOutput.dataset.activeToolTimestamps || "{}";
    const serializedPrompts = this.liveOutput.dataset.activeToolPrompts || "{}";
    delete this.liveOutput.dataset.activeToolEvents;
    delete this.liveOutput.dataset.activeToolTimestamps;
    delete this.liveOutput.dataset.activeToolPrompts;
    try {
      const events = JSON.parse(serializedEvents);
      const timestamps = JSON.parse(serializedTimestamps);
      const prompts = JSON.parse(serializedPrompts);
      if (!Array.isArray(events) || !timestamps || typeof timestamps !== "object" || Array.isArray(timestamps) || !prompts || typeof prompts !== "object" || Array.isArray(prompts)) return;
      events.forEach((event) => this.renderToolExecutionEvent(event, timestamps[event.toolCallId], false, prompts[event.toolCallId]));
    } catch (_error) {
    }
  }

  removePendingCompactionMessage() {
    this.liveOutput?.querySelectorAll('[data-pending-compaction="true"]').forEach((entry) => entry.remove());
  }

  appendPendingCompactionMessage(timestamp = new Date()) {
    if (this.liveOutput?.querySelector('[data-pending-compaction="true"]')) return;
    const entry = this.appendCompactMessage("status", "Compacting conversation…", "Pi is summarizing the conversation so the session can continue with less context.", true, true, timestamp);
    if (entry?.article) entry.article.dataset.pendingCompaction = "true";
  }

  renderCompactionEvent(event) {
    this.removePendingCompactionMessage();
    this.liveCompactionRendered = true;
    this.appendCompactMessage("status", "Conversation compacted", event.summary || "Compaction completed", true, true, eventTimestamp(event));
  }

  renderMessageEvent(event) {
    const message = this.parser.eventMessage(event);
    const segments = message?.content ? this.parser.contentSegments(message.content, message) : [{ text: this.parser.messageText(message), compact: false, summary: "", startIndex: 0, endIndex: 0, finalAssistantResponse: true, images: [] }].filter((segment) => segment.text);
    const roleName = this.parser.liveEventRole(event, message);
    const assistantEnded = roleName === "assistant" && event.type === "message_end";
    const outcome = { roleName, assistantEnded, finalAssistantEnded: assistantEnded && this.parser.eventHasFinalAssistantText(event), rendered: segments.length > 0 };

    if (roleName === "assistant" && event.type === "message_start") {
      this.conversationController.resetOversizedFollow();
      this.clearLiveAssistantStreaming();
      this.resetLiveAssistantTracking();
    }
    if (outcome.assistantEnded) this.clearLiveAssistantStreaming();
    if (segments.length === 0) return outcome;

    const shouldScroll = this.conversationController.followLiveOutput();
    const timestamp = eventTimestamp(event);

    if (roleName !== "assistant") {
      segments.forEach((segment, index) => {
        const toolExecutionEntry = segment.toolCallId && this.liveToolExecutions.get(segment.toolCallId);
        const pairedToolCallEntry = PAIRED_TOOL_NAMES.has(segment.toolName) && segment.toolCallId && this.livePairedToolCalls.get(segment.toolCallId);
        if (toolExecutionEntry && segment.isToolResult) {
          const freshSubagentDetails = segment.toolName === "subagent" && this.parser.richSubagentDetails(message.details);
          const subagentDetails = segment.toolName === "subagent" ? this.retainSubagentDetails(toolExecutionEntry, message.details, message.isError ? "error" : "done") : null;
          const resultText = subagentDetails ? this.parser.subagentDisplayText(subagentDetails, segment.text, false, !freshSubagentDetails) : segment.text;
          const resultSummary = subagentDetails ? this.parser.subagentSummary(subagentDetails, false) : segment.summary;
          this.renderSubagentPrompt(toolExecutionEntry, segment.toolPrompt || this.parser.subagentPromptFromDetails(message.details));
          this.renderToolTranscriptBody(toolExecutionEntry.body, resultText, segment.toolName || toolExecutionEntry.toolName, { preview: segment.toolPreview === true });
          this.renderToolSummary(toolExecutionEntry.summaryText, segment.summaryParts, resultSummary);
          toolExecutionEntry.article.classList.toggle("message--tool-error", segment.error === true);
          if (!this.markLiveEntryRendered(toolExecutionEntry, toolExecutionEntry.article.dataset.role || "toolResult", segment.text, timestamp)) return;
          this.replaceMessageImages(toolExecutionEntry.article, segment.images);
          this.conversationController.afterLiveOutputChange(shouldScroll);
        } else if (pairedToolCallEntry && segment.isToolResult) {
          const mergedText = segment.toolName === "read" && segment.error !== true ? "" : (segment.toolName === "bash" || (segment.toolTranscript && segment.error !== true && segment.toolName !== "write") ? segment.text : [pairedToolCallEntry.body.dataset.rawText, segment.text].filter(Boolean).join("\n\n"));
          const preview = segment.toolPreview === true || (segment.error === true && pairedToolCallEntry.body.classList.contains("message-body--edit-preview"));
          this.renderToolTranscriptBody(pairedToolCallEntry.body, mergedText, segment.toolName || pairedToolCallEntry.toolName, { preview });
          pairedToolCallEntry.article.classList.toggle("message--tool-error", segment.error === true);
          if (!this.markLiveEntryRendered(pairedToolCallEntry, pairedToolCallEntry.article.dataset.role || "assistant", mergedText)) return;
          this.replaceMessageImages(pairedToolCallEntry.article, segment.images);
          this.conversationController.afterLiveOutputChange(shouldScroll);
        } else if (roleName === "user" && !segment.compact) {
          this.upsertLiveUserSegment(event, segment, index, shouldScroll, timestamp);
        } else if (segment.compact) {
          this.appendCompactMessage(roleName, segment.summary, segment.text, true, shouldScroll, timestamp, { summaryParts: segment.summaryParts, toolTranscript: segment.toolTranscript, toolName: segment.toolName, toolPreview: segment.toolPreview, toolPrompt: segment.toolPrompt, error: segment.error, images: segment.images });
        } else {
          this.appendMessage(roleName, segment.text, true, shouldScroll, timestamp, { images: segment.images });
        }
      });
      return outcome;
    }

    this.liveAssistantSeen = true;
    segments.forEach((segment, index) => {
      if (segment.compact) this.clearLiveAssistantStreaming();
      this.upsertLiveAssistantSegment(event, roleName, segment, index, shouldScroll, timestamp);
    });
    return outcome;
  }

}

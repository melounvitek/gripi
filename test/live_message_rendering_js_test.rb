require "minitest/autorun"
require "json"
require "open3"
require "time"

class LiveMessageRenderingJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_markdown_response_from_previous_binding_is_aborted_and_ignored
    result = run_javascript(<<~JS)
      const { ServerMarkdownRenderer } = await import(#{module_url("server_markdown_renderer.js").to_json});
      const requests = [];
      const timers = [];
      globalThis.setTimeout = (callback) => { timers.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.FormData = class { set() {} };
      globalThis.fetch = (_url, options) => new Promise((resolve) => requests.push({ options, resolve }));
      const body = { dataset: {}, innerHTML: "plain", closest: () => null };
      const renderer = new ServerMarkdownRenderer({ createElement() {} }, { autoScrollEnabled: false });
      renderer.bind();
      renderer.render(body, "old session", 0);
      timers.shift()();
      renderer.bind();
      requests[0].resolve({ ok: true, json: async () => ({ html: "<p>stale</p>" }) });
      await Promise.resolve();
      await Promise.resolve();
      console.log(JSON.stringify({ aborted: requests[0].options.signal.aborted, html: body.innerHTML, rendering: body.dataset.rendering }));
    JS

    assert_equal true, result["aborted"]
    assert_equal "plain", result["html"]
    assert_equal "pending", result["rendering"]
  end

  def test_superseded_markdown_failure_does_not_cancel_the_current_render
    result = run_javascript(<<~JS)
      const { ServerMarkdownRenderer } = await import(#{module_url("server_markdown_renderer.js").to_json});
      const requests = [];
      const timers = [];
      globalThis.setTimeout = (callback) => { timers.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.FormData = class { set() {} };
      globalThis.fetch = (_url, options) => new Promise((resolve, reject) => requests.push({ options, resolve, reject }));
      const body = { dataset: {}, innerHTML: "plain", closest: () => null, querySelectorAll: () => [] };
      const renderer = new ServerMarkdownRenderer({ createElement() {} }, { autoScrollEnabled: false });
      renderer.bind();
      renderer.render(body, "first", 0);
      timers.shift()();
      renderer.render(body, "second", 0);
      timers.shift()();
      requests[0].reject(new Error("late failure"));
      await Promise.resolve();
      requests[1].resolve({ ok: true, json: async () => ({ html: "<p>current</p>" }) });
      for (let index = 0; index < 4; index += 1) await Promise.resolve();
      console.log(JSON.stringify({ firstAborted: requests[0].options.signal.aborted, html: body.innerHTML }));
    JS

    assert_equal true, result["firstAborted"]
    assert_equal "<p>current</p>", result["html"]
  end

  def test_current_markdown_failures_restore_plain_text_and_clear_pending_state
    result = run_javascript(<<~JS)
      const { ServerMarkdownRenderer } = await import(#{module_url("server_markdown_renderer.js").to_json});
      const timers = [];
      const responses = [{ ok: false }, new Error("network failure")];
      globalThis.setTimeout = (callback) => { timers.push(callback); return timers.length; };
      globalThis.clearTimeout = () => {};
      globalThis.FormData = class { set() {} };
      globalThis.fetch = async () => { const response = responses.shift(); if (response instanceof Error) throw response; return response; };
      const renderer = new ServerMarkdownRenderer({ createElement() {} }, { autoScrollEnabled: false });
      renderer.bind();
      const httpBody = { dataset: {}, textContent: "", closest: () => null };
      renderer.render(httpBody, "HTTP fallback", 0);
      timers.shift()();
      await Promise.resolve();
      await Promise.resolve();
      const networkBody = { dataset: {}, textContent: "", closest: () => null };
      renderer.render(networkBody, "Network fallback", 0);
      timers.shift()();
      await Promise.resolve();
      await Promise.resolve();
      console.log(JSON.stringify({
        http: { text: httpBody.textContent, pending: httpBody.dataset.rendering || null },
        network: { text: networkBody.textContent, pending: networkBody.dataset.rendering || null }
      }));
    JS

    assert_equal({ "text" => "HTTP fallback", "pending" => nil }, result["http"])
    assert_equal({ "text" => "Network fallback", "pending" => nil }, result["network"])
  end

  def test_live_bash_events_update_one_terminal_aware_shell_card_by_id
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const classes = new Set();
      const status = { hidden: true, children: [], replaceChildren(...children) { this.children = children; } };
      const entry = {
        article: {
          dataset: {},
          classList: {
            add(...names) { names.forEach((name) => classes.add(name)); },
            toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); },
            contains(name) { return classes.has(name); }
          }
        },
        summaryText: { textContent: "" },
        body: {},
        meta: { textContent: "" },
        status
      };
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement() { return { className: "", textContent: "" }; } };
      renderer.parser = new LiveMessageParser("/home/tester");
      renderer.liveBashExecutions = new Map();
      renderer.completedBashExecutionIds = new Set();
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange() {} };
      let appended = 0;
      const outputs = [];
      renderer.appendCompactMessage = (_role, summary, text, _live, _scroll, timestamp, options) => {
        appended += 1;
        entry.summaryText.textContent = summary;
        entry.article.dataset.bashId = options.bashId;
        entry.article.dataset.messageTimestamp = String(new Date(timestamp).getTime() / 1000);
        entry.meta.textContent = `started:${timestamp}`;
        outputs.push(text);
        return entry;
      };
      renderer.renderToolTranscriptBody = (_body, text, toolName) => outputs.push([text, toolName]);

      const started = renderer.renderBashEvent({
        type: "bash_start", bashId: "bash-9", command: "/home/tester/bin/run <unsafe>", excludeFromContext: true, gatewayTimestamp: "2026-06-13T10:00:00Z"
      });
      const running = {
        same: started === entry,
        summary: entry.summaryText.textContent,
        status: status.children.map((item) => item.textContent),
        role: started.article.dataset.bashId,
        timestamp: started.article.dataset.messageTimestamp,
        meta: entry.meta.textContent
      };
      const completed = renderer.renderBashEvent({
        type: "bash_end", bashId: "bash-9", command: "/home/tester/bin/run <unsafe>", excludeFromContext: true,
        result: { output: "progress\\rfinished \\u001b[31mred\\u001b[0m", exitCode: 7, cancelled: true, truncated: true, fullOutputPath: "/home/tester/logs/<unsafe>&.log" }, gatewayTimestamp: "2026-06-13T10:02:00Z"
      });
      const finished = {
        same: completed === entry,
        status: status.children.map((item) => item.textContent),
        classes: [...classes].sort(),
        output: outputs.at(-1),
        timestamp: entry.article.dataset.messageTimestamp,
        fingerprint: entry.article.dataset.messageFingerprint,
        meta: entry.meta.textContent
      };
      renderer.renderBashEvent({
        type: "bash_end", bashId: "bash-9", command: "/home/tester/bin/run <unsafe>", excludeFromContext: true,
        result: { output: "progress\\rfinished \\u001b[31mred\\u001b[0m", exitCode: 7, cancelled: true, truncated: true },
        gatewayTimestamp: "2026-06-13T10:03:00Z"
      });
      renderer.renderBashEvent({
        type: "bash_start", bashId: "bash-9", command: "ignored late start", gatewayTimestamp: "2026-06-13T10:00:00Z"
      });
      console.log(JSON.stringify({
        appended,
        running,
        finished,
        afterLateStart: {
          summary: entry.summaryText.textContent,
          status: status.children.map((item) => item.textContent),
          output: outputs.at(-1)
        }
      }));
    JS

    assert_equal 1, result.fetch("appended")
    assert_equal true, result.dig("running", "same")
    assert_equal "$ ~/bin/run <unsafe>", result.dig("running", "summary")
    assert_equal ["excluded from model context", "running"], result.dig("running", "status")
    assert_equal "bash-9", result.dig("running", "role")
    refute_equal result.dig("running", "timestamp"), result.dig("finished", "timestamp")
    refute_equal result.dig("running", "meta"), result.dig("finished", "meta")
    assert result.dig("finished", "same")
    assert_equal ["excluded from model context", "exit 7", "cancelled", "output truncated", "full output: ~/logs/<unsafe>&.log"], result.dig("finished", "status")
    assert_includes result.dig("finished", "classes"), "message--bash-execution"
    assert_includes result.dig("finished", "classes"), "message--tool-error"
    assert_equal ["progress\rfinished \e[31mred\e[0m", "bash"], result.dig("finished", "output")
    assert_match(/\Atool:#{Time.iso8601("2026-06-13T10:02:00Z").to_i}:/, result.dig("finished", "fingerprint"))
    assert_equal "$ ~/bin/run <unsafe>", result.dig("afterLateStart", "summary")
    assert_equal ["excluded from model context", "exit 7", "cancelled", "output truncated", "full output: ~/logs/<unsafe>&.log"], result.dig("afterLateStart", "status")
    assert_equal ["progress\rfinished \e[31mred\e[0m", "bash"], result.dig("afterLateStart", "output")
  end

  def test_active_bash_snapshot_is_consumed_and_rendered_as_a_start_event
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.liveOutput = { dataset: {
        activeBash: JSON.stringify({
          bash_id: "bash-3", command: "sleep 30", exclude_from_context: true, started_at: "2026-06-13T10:00:00Z"
        }),
        completedBashEvents: JSON.stringify([{ type: "bash_end", bashId: "bash-2", command: "pwd", result: { output: "/tmp" } }]),
        persistedBashExecutions: JSON.stringify(["bash-1"])
      } };
      const rendered = [];
      renderer.completedBashExecutionIds = new Set();
      renderer.renderBashEvent = (event) => { rendered.push(event); };
      const persisted = renderer.restorePersistedBashExecutions();
      const completed = renderer.restoreCompletedBashExecutions();
      const restored = renderer.restoreActiveBash();
      console.log(JSON.stringify({
        restored,
        completed,
        persisted,
        rendered,
        consumed: renderer.liveOutput.dataset.activeBash === undefined && renderer.liveOutput.dataset.completedBashEvents === undefined && renderer.liveOutput.dataset.persistedBashExecutions === undefined
      }));
    JS

    assert result.fetch("consumed")
    assert_equal "bash_start", result.dig("restored", "type")
    assert_equal "bash-3", result.dig("restored", "bashId")
    assert_equal "bash-1", result.dig("persisted", 0)
    assert_equal "bash-2", result.dig("completed", 0, "bashId")
    assert_equal "bash_end", result.dig("rendered", 0, "type")
    assert_equal "sleep 30", result.dig("rendered", 1, "command")
    assert result.dig("rendered", 1, "excludeFromContext")
    assert_equal 1_781_344_800_000, result.dig("rendered", 1, "gatewayTimestamp")
  end

  def test_parser_only_honors_phases_from_valid_v1_signatures
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const parser = new LiveMessageParser();
      const replyText = (text, textSignature) => parser.finalAssistantReplyText({
        role: "assistant",
        content: [{ type: "text", text, ...(textSignature === undefined ? {} : { textSignature }) }]
      });
      const signature = (id, phase) => JSON.stringify({ v: 1, id, ...(phase ? { phase } : {}) });
      console.log(JSON.stringify({
        commentary: replyText("Working", signature("progress", "commentary")),
        final: replyText("Finished", signature("answer", "final_answer")),
        unsigned: replyText("Unsigned"),
        opaque: replyText("Opaque", "opaque-signature"),
        unphased: replyText("Unphased", signature("unphased")),
        malformed: replyText("Malformed", "{broken"),
        missingId: replyText("Missing ID", JSON.stringify({ v: 1, phase: "commentary" })),
        future: replyText("Future", JSON.stringify({ v: 2, id: "future", phase: "commentary" }))
      }));
    JS

    assert_equal "", result["commentary"]
    assert_equal "Finished", result["final"]
    assert_equal "Unsigned", result["unsigned"]
    assert_equal "Opaque", result["opaque"]
    assert_equal "Unphased", result["unphased"]
    assert_equal "Malformed", result["malformed"]
    assert_equal "Missing ID", result["missingId"]
    assert_equal "Future", result["future"]
  end

  def test_expanded_skill_prompt_reuses_the_optimistic_command_message
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const expandedSkill = `<skill name="diffx" location="/home/tester/.pi/agent/skills/diffx/SKILL.md">
References are relative to /home/tester/.pi/agent/skills/diffx.

# diffx

Start a review.
</skill>`;
      const optimisticArticle = {
        dataset: { optimistic: "true", optimisticText: "/skill:diffx", optimisticImageCount: "0" },
        hasAttribute: (name) => name === "data-optimistic-text",
        querySelector: (selector) => selector === ".message-body" ? { textContent: "/skill:diffx" } : null
      };
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = new LiveMessageParser();
      renderer.conversationController = { followLiveOutput: () => true, afterLiveOutputChange() {} };
      renderer.liveUserMessages = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.livePairedToolCalls = new Map();
      renderer.conversationScroll = { querySelectorAll: () => [optimisticArticle] };
      renderer.replaceMessageImages = () => {};
      let appended = 0;
      renderer.appendMessage = () => { appended += 1; return null; };

      renderer.renderMessageEvent({
        type: "message_start",
        message: { id: "user-1", role: "user", content: [{ type: "text", text: expandedSkill }] }
      });

      console.log(JSON.stringify({
        text: optimisticArticle.querySelector(".message-body").textContent,
        optimistic: optimisticArticle.dataset.optimistic || null,
        appended
      }));
    JS

    assert_equal({ "text" => "/skill:diffx", "optimistic" => nil, "appended" => 0 }, result)
  end

  def test_live_skill_prompt_preserves_arguments_and_ignores_other_xml
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const parser = new LiveMessageParser();
      const expandedSkill = `<skill name="pdf-tools" location="/skills/pdf/SKILL.md">
References are relative to /skills/pdf.

# PDF tools

Extract PDFs.
</skill>`;
      const text = (value) => parser.messageText({ role: "user", content: [{ type: "text", text: value }] });
      const invalidSkills = [
        expandedSkill.replace("relative to /skills/pdf.", "relative to /skills/other."),
        expandedSkill.replaceAll("/skills/pdf", "skills/pdf"),
        expandedSkill.replaceAll("/skills/pdf", "/skills/../pdf"),
        expandedSkill.replaceAll("/skills/pdf", "/skills/./pdf"),
        expandedSkill.replaceAll("/skills/pdf", "/skills//pdf"),
        expandedSkill.replace('location="/skills/pdf/SKILL.md"', 'location="/skills/pdf/"')
      ];
      console.log(JSON.stringify({
        command: text(expandedSkill),
        arguments: text(`${expandedSkill}\n\nextract report.pdf`),
        invalidUnchanged: invalidSkills.every((value) => text(value) === value),
        trailingNewline: text(`${expandedSkill}\n`),
        ordinary: text(`<skill name="pdf-tools">ordinary text</skill>`)
      }));
    JS

    assert_equal "/skill:pdf-tools", result["command"]
    assert_equal "/skill:pdf-tools extract report.pdf", result["arguments"]
    assert result["invalidUnchanged"]
    assert_equal "</skill>\n", result["trailingNewline"][-9..]
    assert_equal '<skill name="pdf-tools">ordinary text</skill>', result["ordinary"]
  end

  def test_live_renderer_only_marks_final_answer_message_ends_as_final
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const render = (phase) => {
        const renderer = Object.create(LiveMessageRenderer.prototype);
        renderer.parser = new LiveMessageParser();
        renderer.conversationController = { followLiveOutput: () => true };
        renderer.liveAssistantSegments = new Map();
        renderer.livePairedToolCalls = new Map();
        renderer.liveToolExecutions = new Map();
        renderer.liveUserMessages = new Map();
        renderer.clearLiveAssistantStreaming = () => {};
        renderer.liveMessageAlreadyRendered = () => false;
        renderer.appendMessage = (_role, _text, _live, _scroll, _timestamp, options) => ({
          article: { classList: { toggle() {} }, dataset: {} },
          options
        });
        renderer.updateLiveSegment = (entry) => entry;
        const message = {
          role: "assistant",
          content: [{ type: "text", text: phase, textSignature: JSON.stringify({ v: 1, id: phase, phase }) }]
        };
        renderer.renderMessageEvent({ type: "message_update", message });
        const outcome = renderer.renderMessageEvent({ type: "message_end", message });
        const entry = [...renderer.liveAssistantSegments.values()][0];
        return { outcome, marked: entry.article.dataset.finalAssistantResponse || null, streamingOption: entry.options.finalAssistantResponse };
      };
      console.log(JSON.stringify({ commentary: render("commentary"), final: render("final_answer") }));
    JS

    assert_equal false, result.dig("commentary", "outcome", "finalAssistantEnded")
    assert_nil result.dig("commentary", "marked")
    assert_equal false, result.dig("commentary", "streamingOption")
    assert_equal true, result.dig("final", "outcome", "finalAssistantEnded")
    assert_equal "true", result.dig("final", "marked")
    assert_equal false, result.dig("final", "streamingOption")
  end

  def test_live_assistant_cumulative_updates_keep_reasoning_and_final_text_separate
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = new LiveMessageParser();
      renderer.conversationController = { followLiveOutput: () => true, resetOversizedFollow() {}, scheduleFocusedActivityRefresh() {} };
      renderer.liveAssistantSegments = new Map();
      renderer.livePairedToolCalls = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.liveUserMessages = new Map();
      renderer.liveCustomMessages = new Map();
      renderer.clearLiveAssistantStreaming = () => {};
      renderer.liveMessageAlreadyRendered = () => false;
      renderer.replaceMessageImages = () => {};
      renderer.updateLiveSegment = (entry) => entry;
      const appended = [];
      renderer.appendMessage = (_role, text, _live, _scroll, _timestamp, options) => {
        const entry = { text, options, compact: false, article: { dataset: {}, classList: { toggle() {}, remove() {} } } };
        appended.push(entry);
        return entry;
      };
      const part = (text, id, phase) => ({ type: "text", text, textSignature: JSON.stringify({ v: 1, id, phase }) });
      const thinking = { type: "thinking", thinking: "Checking" };
      const answer = part("Finished", "answer", "final_answer");
      renderer.renderMessageEvent({ type: "message_start", message: { role: "assistant", content: [thinking] } });
      renderer.renderMessageEvent({ type: "message_update", message: { role: "assistant", content: [thinking, answer] }, assistantMessageEvent: { contentIndex: 1, partial: { role: "assistant", content: [thinking, answer] } } });
      renderer.renderMessageEvent({ type: "message_end", message: { role: "assistant", content: [thinking, answer] } });
      console.log(JSON.stringify({
        entries: appended.map((entry) => ({ text: entry.text, thinking: entry.options.thinking, final: entry.article.dataset.finalAssistantResponse || null })),
        tracked: renderer.liveAssistantSegments.size
      }));
    JS

    assert_equal 2, result.fetch("tracked")
    assert_equal({ "text" => "Checking", "thinking" => true, "final" => nil }, result.fetch("entries")[0])
    assert_equal({ "text" => "Finished", "thinking" => false, "final" => "true" }, result.fetch("entries")[1])
  end

  def test_custom_message_lifecycle_renders_once_and_respects_display
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = new LiveMessageParser();
      renderer.conversationController = { followLiveOutput: () => true };
      renderer.liveAssistantSegments = new Map();
      renderer.livePairedToolCalls = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.liveUserMessages = new Map();
      renderer.liveCustomMessages = new Map();
      renderer.liveMessageAlreadyRendered = () => false;
      renderer.updateLiveSegment = (entry, _role, segment) => { entry.text = segment.text; return entry; };
      const appended = [];
      renderer.appendMessage = (role, text, _live, _scroll, _timestamp, options) => {
        const entry = { role, text, customType: options.customType, article: { dataset: {}, classList: { toggle() {} } } };
        appended.push(entry);
        return entry;
      };

      const visible = {
        role: "custom",
        customType: "session-title-update",
        content: "Session renamed",
        display: true,
        timestamp: 1781344860100
      };
      renderer.renderMessageEvent({ type: "message_start", message: visible });
      renderer.renderMessageEvent({ type: "message_end", message: visible });
      renderer.renderMessageEvent({
        type: "message_end",
        message: { ...visible, timestamp: 1781344860200 }
      });
      renderer.renderMessageEvent({
        type: "message_end",
        message: { ...visible, content: "Hidden context", display: false, timestamp: 1781344860300 }
      });
      console.log(JSON.stringify(appended.map(({ role, text, customType }) => ({ role, text, customType }))));
    JS

    assert_equal [
      { "role" => "custom", "text" => "Session renamed", "customType" => "session-title-update" },
      { "role" => "custom", "text" => "Session renamed", "customType" => "session-title-update" }
    ], result
  end

  def test_top_level_displayed_custom_message_renders_live
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const rendered = [];
      renderer.renderMessageEvent = (event) => { rendered.push(event); return event; };
      const visible = renderer.renderCustomMessageEvent({ type: "custom_message", id: "custom-1", customType: "notice", content: "Visible notice", display: true, timestamp: 1781344860100 });
      const hidden = renderer.renderCustomMessageEvent({ type: "custom_message", content: "Hidden notice", display: false });
      console.log(JSON.stringify({ visible, hidden: hidden || null, rendered }));
    JS

    assert_equal 1, result.fetch("rendered").length
    message = result.dig("rendered", 0, "message")
    assert_equal "custom", message.fetch("role")
    assert_equal "Visible notice", message.fetch("content")
    assert_equal true, message.fetch("display")
    assert_nil result["hidden"]
  end

  def test_unmatched_live_tool_result_preserves_tool_call_identity
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = {
        eventMessage: (event) => event.message,
        contentSegments: () => [{ compact: true, isToolResult: true, toolCallId: "call-1", toolName: "custom-tool", summary: "custom-tool", text: "failed", error: true, images: [] }],
        liveEventRole: () => "toolResult",
        eventHasFinalAssistantText: () => false
      };
      renderer.conversationController = { followLiveOutput: () => false };
      renderer.liveToolExecutions = new Map();
      renderer.livePairedToolCalls = new Map();
      let options;
      renderer.appendCompactMessage = (_role, _summary, _text, _live, _scroll, _timestamp, value) => { options = value; };
      renderer.renderMessageEvent({ type: "message_end", timestamp: "2026-06-13T10:00:00Z", message: { role: "toolResult", content: [{}] } });
      console.log(JSON.stringify(options));
    JS

    assert_equal "call-1", result.fetch("toolCallId")
    assert_equal true, result.fetch("error")
  end

  def test_removing_pending_compaction_refreshes_focused_activity
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      let removed = 0;
      let refreshes = 0;
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.liveOutput = { querySelectorAll: () => [{ remove() { removed += 1; } }] };
      renderer.conversationController = { scheduleFocusedActivityRefresh() { refreshes += 1; } };
      renderer.removePendingCompactionMessage();
      console.log(JSON.stringify({ removed, refreshes }));
    JS

    assert_equal({ "removed" => 1, "refreshes" => 1 }, result)
  end

  def test_live_compaction_renders_native_and_legacy_summaries_as_collapsed_details
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      class Element {
        constructor(tagName) { this.tagName = tagName.toUpperCase(); this.children = []; this.dataset = {}; this.attributes = {}; this.className = ""; this.textContent = ""; }
        append(...children) { this.children.push(...children); }
        replaceChildren(...children) { this.children = children; }
        setAttribute(name, value) { this.attributes[name] = value; }
      }
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement: (tagName) => new Element(tagName) };
      renderer.liveOutput = new Element("div");
      renderer.conversationController = { followLiveOutput: () => true, afterLiveOutputChange() {} };
      renderer.liveMessageAlreadyRendered = () => false;
      renderer.renderMessageImages = () => {};
      renderer.removePendingCompactionMessage = () => {};
      const render = (event) => {
        const entry = renderer.renderCompactionEvent(event);
        const summary = entry.details.children[0];
        return {
          detailsTag: entry.details.tagName,
          detailsClass: entry.details.className,
          open: Object.hasOwn(entry.details.attributes, "open"),
          summaryTag: summary.tagName,
          title: summary.children[0].textContent,
          actionClass: summary.children[1].className,
          articleClass: entry.article.className,
          text: entry.body.textContent
        };
      };
      const native = render({ type: "compaction_end", result: { summary: "## Goal\\nNative summary" } });
      const duplicate = renderer.renderCompactionEvent({ type: "compaction", summary: "## Goal\\nNative summary" });
      console.log(JSON.stringify({
        native,
        duplicate: duplicate || null,
        failed: renderer.renderCompactionEvent({ type: "compaction_end", result: null, errorMessage: "Compaction failed", gatewayTimestamp: 1781344865000 }) || null,
        legacy: render({ type: "compaction", summary: "Legacy summary", gatewayTimestamp: 1781344880200 })
      }));
    JS

    assert_includes result.dig("native", "articleClass"), "message--compaction"
    assert_equal "DETAILS", result.dig("native", "detailsTag")
    assert_equal "message-details message-details--compaction", result.dig("native", "detailsClass")
    assert_equal false, result.dig("native", "open")
    assert_equal "SUMMARY", result.dig("native", "summaryTag")
    assert_equal "Conversation compacted", result.dig("native", "title")
    assert_equal "compaction-details-action", result.dig("native", "actionClass")
    assert_equal "## Goal\nNative summary", result.dig("native", "text")
    assert_nil result["duplicate"]
    assert_nil result["failed"]
    assert_equal "Legacy summary", result.dig("legacy", "text")
  end

  def test_server_terminal_source_hydrates_through_the_shared_screen_renderer
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const reset = "\\x1b[3J\\x1b[2J\\x1b[H";
      const source = `${reset}history one\\nstale screen${reset}history one\\nhistory two\\n\\x1b[32mcurrent screen\\x1b[0m`;
      const body = { dataset: { terminalOutputSource: Buffer.from(source).toString("base64"), terminalToolName: "bash" } };
      const renderer = Object.create(LiveMessageRenderer.prototype);
      let followChanges = 0;
      let rendered;
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange: () => { followChanges += 1; } };
      renderer.renderResolvedToolTranscriptBody = (_body, lines, rawText, toolName) => {
        rendered = { lines: lines.map((line) => line.text), rawText, toolName };
      };
      await renderer.hydrateTerminalOutputs({ querySelectorAll: () => [body] });
      console.log(JSON.stringify({ rendered, sourceRemoved: body.dataset.terminalOutputSource === undefined, followChanges }));
    JS

    assert_equal({
      "lines" => ["history one", "history two", "current screen"],
      "rawText" => "history one\nhistory two\ncurrent screen",
      "toolName" => "bash"
    }, result["rendered"])
    assert_equal true, result["sourceRemoved"]
    assert_equal 1, result["followChanges"]
  end

  def test_pending_message_queue_hydrates_and_tracks_authoritative_updates
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const makeElement = () => ({ className: "", textContent: "", children: [], append(...children) { this.children.push(...children); } });
      const pending = { hidden: true, children: [], replaceChildren(...children) { this.children = children; } };
      const liveOutput = { dataset: { queuedMessages: JSON.stringify({ steering: ["First", "First"], followUp: ["Later"] }) } };
      const document = {
        getElementById(id) { return id === "live-output" ? liveOutput : null; },
        querySelector(selector) { return selector === "[data-pending-messages]" ? pending : null; },
        createElement: makeElement
      };
      const renderer = new LiveMessageRenderer(document, { element: {} }, {}, { bind() {} });

      renderer.bind();
      const hydrated = pending.children.map((row) => ({ className: row.className, text: row.textContent }));
      renderer.renderQueuedMessages({ steering: ["Next"], followUp: [] });
      const updated = pending.children.map((row) => row.textContent);
      renderer.renderQueuedMessages({ steering: [], followUp: [] });

      console.log(JSON.stringify({ hydrated, updated, hiddenAfterClear: pending.hidden, countAfterClear: pending.children.length }));
    JS

    assert_equal [
      { "className" => "pending-message pending-message--steering", "text" => "Steering: First" },
      { "className" => "pending-message pending-message--steering", "text" => "Steering: First" },
      { "className" => "pending-message pending-message--follow-up", "text" => "Follow-up: Later" }
    ], result.fetch("hydrated")
    assert_equal ["Steering: Next"], result.fetch("updated")
    assert_equal true, result.fetch("hiddenAfterClear")
    assert_equal 0, result.fetch("countAfterClear")
  end

  def test_parser_semantics_cover_representative_ssr_message_shapes
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { messageFingerprint } = await import(#{module_url("formatting.js").to_json});
      const parser = new LiveMessageParser("/home/tester");
      const assistant = parser.contentSegments([{ type: "text", text: "Answer" }], { role: "assistant" });
      const thinking = parser.contentSegments([{ type: "thinking", thinking: "Consider" }], { role: "assistant" });
      const tool = parser.contentSegments([{ type: "toolCall", name: "read", id: "r1", arguments: { path: "/home/tester/a" } }], { role: "assistant" });
      const subagentCall = parser.contentSegments([{ type: "toolCall", name: "subagent", id: "s1", arguments: { task: "Review" } }], { role: "assistant" });
      const subagentResult = parser.contentSegments([{ type: "text", text: "Done" }], { role: "toolResult", toolName: "subagent", toolCallId: "s1", details: { task: "Review" } });
      const images = parser.contentSegments([{ type: "text", text: "Image" }, { type: "image", mimeType: "image/png", data: "cG5n" }], { role: "user" });
      console.log(JSON.stringify({ assistant, thinking, tool, subagentCall, subagentResult, images, fingerprint: messageFingerprint("assistant", "Answer", "2026-01-01T00:00:00.000Z") }));
    JS

    assert_equal "Answer", result.dig("assistant", 0, "text")
    assert_equal true, result.dig("thinking", 0, "thinking")
    assert_equal true, result.dig("tool", 0, "compact")
    assert_equal "~/a", result.dig("tool", 0, "summaryParts", "path")
    assert_empty result["subagentCall"]
    assert_equal "Review", result.dig("subagentResult", 0, "toolPrompt")
    assert_equal "data:image/png;base64,cG5n", result.dig("images", 0, "images", 0, "src")
    assert_match(/\Aassistant:/, result["fingerprint"])

    ssr = File.read(File.expand_path("../views/_message_article.erb", __dir__))
    %w[data-role data-message-fingerprint message-body--thinking message-details--always-open message-images data-subagent-prompt].each do |semantic|
      assert_includes ssr, semantic
    end
  end

  private

  def module_url(name) = "file://#{File.join(ASSETS, name)}"

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end

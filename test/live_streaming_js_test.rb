require "minitest/autorun"
require "json"
require "open3"

class LiveStreamingJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_parser_preserves_supported_images_and_interprets_thinking_and_tools
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const parser = new LiveMessageParser("/home/tester");
      const segments = parser.contentSegments([
        { type: "thinking", thinking: "**Reasoning**\\n\\nInspect this" },
        { type: "toolCall", name: "read", id: "read-1", arguments: { path: "/home/tester/file", offset: 2, limit: 3 } },
        { type: "image", data: "cG5n", mimeType: "image/png" },
        { type: "image", data: "c3Zn", mimeType: "image/svg+xml" }
      ], { role: "assistant" });
      console.log(JSON.stringify(segments));
    JS

    assert_equal 2, result.length
    assert_equal true, result[0]["thinking"]
    assert_equal "Inspect this", result[0]["text"]
    assert_equal({ "name" => "read", "path" => "~/file", "range" => "2-4" }, result[1]["summaryParts"])
    assert_equal ["data:image/png;base64,cG5n"], result[1]["images"].map { |image| image["src"] }
  end

  def test_renderer_streaming_state_is_cleared_before_a_new_assistant_message
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const removed = [];
      const stale = article("stale");
      const tracked = article("tracked");
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.conversationScroll = { querySelectorAll: () => [stale] };
      renderer.liveAssistantSegments = new Map([["one", { article: tracked }]]);
      renderer.livePairedToolCalls = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.liveUserMessages = new Map();
      renderer.clearLiveAssistantStreaming();
      renderer.resetLiveAssistantTracking();
      console.log(JSON.stringify({ removed, segments: renderer.liveAssistantSegments.size, seen: renderer.liveAssistantSeen }));
      function article(name) { return { classList: { remove(value) { removed.push([name, value]); } } }; }
    JS

    assert_equal [["stale", "message--streaming"], ["tracked", "message--streaming"]], result["removed"]
    assert_equal 0, result["segments"]
    assert_equal false, result["seen"]
  end

  def test_renderer_streams_bash_progress_into_its_existing_tool_card
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const classes = new Set();
      const entry = {
        article: {
          classList: {
            contains: (name) => classes.has(name),
            toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); }
          },
          dataset: { role: "assistant" }
        },
        body: { classList: { contains: () => false } },
        summaryText: {},
        toolName: "bash"
      };
      let appended = 0;
      renderer.parser = new LiveMessageParser();
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange() {} };
      renderer.conversationScroll = { querySelectorAll: () => [] };
      renderer.liveAssistantSegments = new Map();
      renderer.livePairedToolCalls = new Map();
      renderer.liveToolExecutions = new Map();
      renderer.liveUserMessages = new Map();
      renderer.clearLiveAssistantStreaming = () => {};
      renderer.liveMessageAlreadyRendered = () => false;
      renderer.appendCompactMessage = (_role, summary) => { appended += 1; entry.summary = summary; return entry; };
      renderer.renderToolTranscriptBody = (body, text) => { body.text = text; };
      renderer.markLiveEntryRendered = () => true;
      renderer.replaceMessageImages = () => {};

      renderer.renderMessageEvent({
        type: "message_end",
        message: { role: "assistant", content: [{ type: "toolCall", id: "bash-1", name: "bash", arguments: { command: "ping 1.1.1.1" } }] }
      });
      renderer.renderToolExecutionEvent({ type: "tool_execution_start", toolCallId: "bash-1", toolName: "bash", args: { command: "ping 1.1.1.1" } });
      const started = entry.body.text;
      renderer.renderToolExecutionEvent({ type: "tool_execution_update", toolCallId: "bash-1", toolName: "bash", partialResult: { content: [{ type: "text", text: "reply 1" }] } });
      const first = entry.body.text;
      renderer.renderToolExecutionEvent({ type: "tool_execution_update", toolCallId: "bash-1", toolName: "bash", partialResult: { content: [{ type: "text", text: "reply 1\\nreply 2" }] } });
      const latest = entry.body.text;
      renderer.renderToolExecutionEvent({ type: "tool_execution_end", toolCallId: "bash-1", toolName: "bash", result: { content: [{ type: "text", text: "event error" }] }, isError: true });
      const ended = entry.body.text;
      const erroredAtEnd = classes.has("message--tool-error");
      const resultMessage = { role: "toolResult", toolCallId: "bash-1", toolName: "bash", content: [{ type: "text", text: "canonical error" }], isError: true };
      renderer.renderMessageEvent({ type: "message_start", message: resultMessage });
      const canonicalAtStart = entry.body.text;
      renderer.renderMessageEvent({ type: "message_end", message: resultMessage });
      console.log(JSON.stringify({ appended, summary: entry.summary, started, first, latest, ended, erroredAtEnd, canonicalAtStart, final: entry.body.text, finalError: classes.has("message--tool-error") }));
    JS

    assert_equal 1, result["appended"]
    assert_equal "$ ping 1.1.1.1", result["summary"]
    assert_equal "(running…)", result["started"]
    assert_equal "reply 1", result["first"]
    assert_equal "reply 1\nreply 2", result["latest"]
    assert_equal "event error", result["ended"]
    assert_equal true, result["erroredAtEnd"]
    assert_equal "canonical error", result["canonicalAtStart"]
    assert_equal "canonical error", result["final"]
    assert_equal true, result["finalError"]
  end

  def test_renderer_coalesces_terminal_updates_and_only_applies_the_latest_screen
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const body = {};
      const rendered = [];
      let followChanges = 0;
      renderer.parser = { displayHomePath: (text) => text };
      renderer.terminalRenderStates = new WeakMap();
      renderer.conversationController = {
        followLiveOutput: () => false,
        afterLiveOutputChange: () => { followChanges += 1; }
      };
      renderer.renderResolvedToolTranscriptBody = (_body, lines, rawText) => {
        rendered.push({ lines: lines.map((line) => line.text ?? line), rawText });
      };

      renderer.renderToolTranscriptBody(body, "first 10%\\rfirst 20%", "bash");
      renderer.renderToolTranscriptBody(body, "old\\x1b[2J\\x1b[Hlatest", "bash");
      while (renderer.terminalRenderStates.get(body).rendering) await new Promise((resolve) => setTimeout(resolve, 1));
      console.log(JSON.stringify({ rendered, followChanges }));
    JS

    assert_equal [{ "lines" => ["latest"], "rawText" => "latest" }], result["rendered"]
    assert_equal 1, result["followChanges"]
  end

  def test_renderer_applies_the_latest_cumulative_terminal_transcript_snapshot
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const body = {};
      const rendered = [];
      const reset = "\\x1b[3J\\x1b[2J\\x1b[H";
      const first = `${reset}history one\\nhistory two\\nstale screen`;
      const latest = `${first}${reset}history one\\nhistory two\\nhistory three\\n\\x1b[32mcurrent screen\\x1b[0m`;
      renderer.parser = { displayHomePath: (text) => text };
      renderer.terminalRenderStates = new WeakMap();
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange() {} };
      renderer.renderResolvedToolTranscriptBody = (_body, lines, rawText) => {
        rendered.push({ lines: lines.map((line) => line.text), rawText });
      };

      renderer.renderToolTranscriptBody(body, first, "bash");
      renderer.renderToolTranscriptBody(body, latest, "bash");
      while (renderer.terminalRenderStates.get(body).rendering) await new Promise((resolve) => setTimeout(resolve, 1));
      console.log(JSON.stringify(rendered));
    JS

    assert_equal [{
      "lines" => ["history one", "history two", "history three", "current screen"],
      "rawText" => "history one\nhistory two\nhistory three\ncurrent screen"
    }], result
  end

  def test_renderer_drops_terminal_work_from_an_old_session_binding
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const body = {};
      let rendered = 0;
      let followChanges = 0;
      renderer.terminalBindingGeneration = 1;
      renderer.terminalRenderStates = new WeakMap();
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange: () => { followChanges += 1; } };
      renderer.renderResolvedToolTranscriptBody = () => { rendered += 1; };
      renderer.renderToolTranscriptBody(body, "old\\rnew", "bash");
      renderer.terminalBindingGeneration = 2;
      while (renderer.terminalRenderStates.get(body).rendering) await new Promise((resolve) => setTimeout(resolve, 1));
      console.log(JSON.stringify({ rendered, followChanges }));
    JS

    assert_equal({ "rendered" => 0, "followChanges" => 0 }, result)
  end

  def test_renderer_builds_safe_styled_dom_for_terminal_lines
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      class Element {
        constructor() { this.children = []; this.className = ""; this.dataset = {}; this.style = {}; this.textContent = ""; }
        append(...children) { this.children.push(...children); }
        replaceChildren(...children) { this.children = children; }
        closest() { return null; }
        classList = { add: (name) => { this.className += ` ${name}`; }, toggle: () => {} };
      }
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement: () => new Element() };
      renderer.parser = { displayHomePath: (text) => text };
      renderer.terminalRenderStates = new WeakMap();
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange() {} };
      const body = new Element();
      renderer.renderToolTranscriptBody(body, "\\x1b[1;31mred\\x1b[0m plain", "bash");
      while (renderer.terminalRenderStates.get(body).rendering) await new Promise((resolve) => setTimeout(resolve, 1));
      const line = body.children[0].children[0];
      const styled = line.children[0];
      console.log(JSON.stringify({ rawText: body.dataset.rawText, lineClass: line.className, text: styled.textContent, color: styled.style.color, styledClass: styled.className, plain: line.children[1] }));
    JS

    assert_equal "red plain", result["rawText"]
    assert_includes result["lineClass"], "tool-output-line--terminal"
    assert_equal "red", result["text"]
    assert_equal "#cd0000", result["color"]
    assert_includes result["styledClass"], "terminal-output-run--bold"
    assert_equal " plain", result["plain"]
  end

  def test_renderer_leaves_file_tool_control_characters_on_the_plain_path
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const captured = [];
      renderer.renderResolvedToolTranscriptBody = (_body, lines, rawText, toolName) => captured.push({ lines, rawText, toolName });
      ["read", "edit", "write"].forEach((toolName) => renderer.renderToolTranscriptBody({}, "literal\\x1b[31m", toolName));
      console.log(JSON.stringify(captured));
    JS

    assert_equal %w[read edit write], result.map { |entry| entry["toolName"] }
    assert result.all? { |entry| entry["lines"] == ["literal\e[31m"] }
  end

  def test_renderer_does_not_overwrite_other_paired_tool_previews
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      const entries = new Map(["read", "edit", "write"].map((name) => [`${name}-1`, { body: { text: `${name} preview` } }]));
      renderer.parser = new LiveMessageParser();
      renderer.livePairedToolCalls = entries;
      renderer.conversationController = { followLiveOutput: () => false, afterLiveOutputChange() {} };
      renderer.renderToolTranscriptBody = (body, text) => { body.text = text; };
      ["read", "edit", "write"].forEach((name) => renderer.renderToolExecutionEvent({
        type: "tool_execution_update",
        toolCallId: `${name}-1`,
        toolName: name,
        partialResult: { content: [{ type: "text", text: `${name} result` }] }
      }));
      console.log(JSON.stringify({ texts: [...entries.values()].map((entry) => entry.body.text) }));
    JS

    assert_equal ["read preview", "edit preview", "write preview"], result["texts"]
  end

  def test_renderer_replaces_live_message_images
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement: (tagName) => ({ tagName, children: [], append(...children) { this.children.push(...children); }, addEventListener() {} }) };
      let container = null;
      const article = { querySelector: () => container, append(value) { container = value; } };
      renderer.replaceMessageImages(article, [{ src: "data:image/png;base64,cG5n", alt: "Attached image" }]);
      const firstCount = container.children.length;
      container.remove = () => { container = null; };
      renderer.replaceMessageImages(article, []);
      console.log(JSON.stringify({ firstCount, cleared: container === null }));
    JS

    assert_equal 1, result["firstCount"]
    assert_equal true, result["cleared"]
  end

  def test_renderer_limits_intrinsic_width_wrapper_to_diff_tool_outputs
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = { displayHomePath: (text) => text.replace("/home/tester", "~") };
      renderer.document = { createElement: () => ({ className: "", children: [], classList: { add() {} }, textContent: "", append(...children) { this.children.push(...children); } }) };
      const body = () => ({
        dataset: {},
        classList: { toggle() {} },
        closest: () => null,
        replaceChildren(...children) { this.children = children; }
      });
      const readBody = body();
      const editBody = body();
      renderer.renderToolTranscriptBody(readBody, "/home/tester/one\\n/home/tester/two", "read");
      renderer.renderToolTranscriptBody(editBody, "+one\\n-two", "edit");
      console.log(JSON.stringify({
        readWrapperClass: readBody.children[0].className,
        readLines: readBody.children[0].children.map((child) => child.textContent),
        editWrapperClass: editBody.children[0].className
      }));
    JS

    assert_equal "tool-output-content", result["readWrapperClass"]
    assert_equal ["~/one", "~/two"], result["readLines"]
    assert_equal "tool-output-content tool-output-content--diff", result["editWrapperClass"]
  end

  def test_optimistic_uploaded_images_remain_owned_by_renderer
    script = File.read(File.join(ASSETS, "live_message_renderer.js"))
    app = File.read(File.join(ASSETS, "app.js"))

    assert_includes script, "article.dataset.optimisticImageCount = String(options.images?.length || 0);"
    assert_includes script, 'return targetText.startsWith(`${optimisticText}\\n`);'
    assert_includes app, "const optimisticImages = pendingImages.map"
    assert_includes app, "liveMessageRenderer.appendMessage(\"user\""
  end

  private

  def module_url(name)
    "file://#{File.join(ASSETS, name)}"
  end

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end

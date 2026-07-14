require "minitest/autorun"
require "json"
require "open3"

class LiveStreamingJsToolEventsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)
  STYLESHEET_PATH = File.join(ASSETS, "app.css")

  def test_subagent_parser_prefers_call_arguments_and_retains_rich_progress
    result = run_javascript(<<~JS)
      const { LiveMessageParser } = await import(#{module_url("live_message_parser.js").to_json});
      const parser = new LiveMessageParser();
      const details = { status: "running", tools: [], textItems: ["Full fresh answer"], streamingText: "", usage: {}, model: "provider/model" };
      const retainedDetails = parser.retainedSubagentDetails(null, details);
      console.log(JSON.stringify({
        fromArgs: parser.subagentPromptFromEvent({ args: { task: "Original" }, partialResult: { details: { task: "Result" } } }),
        fromDetails: parser.subagentPromptFromEvent({ result: { details: { task: "Retained" } } }),
        restored: parser.subagentPromptFromEvent({ result: { details: { task: "Truncated" } } }, "Complete"),
        running: parser.subagentDisplayText(details, "Final", true),
        fresh: parser.subagentDisplayText(details, "Truncated final", false),
        retained: parser.subagentDisplayText(retainedDetails, "Canonical final", false, true)
      }));
    JS

    assert_equal "Original", result["fromArgs"]
    assert_equal "Retained", result["fromDetails"]
    assert_equal "Complete", result["restored"]
    refute_includes result["running"], "Final"
    assert_includes result["fresh"], "Full fresh answer"
    assert_includes result["retained"], "Canonical final"
  end

  def test_subagent_prompt_renderer_keeps_original_prompt
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.document = { createElement: (tagName) => element(tagName) };
      const entry = { details: { insertBefore(value) { this.prompt = value; } }, output: {} };
      renderer.renderSubagentPrompt(entry, "Original delegated prompt");
      renderer.renderSubagentPrompt(entry, "Reconstructed result prompt");
      console.log(JSON.stringify({ text: entry.subagentPromptPreview.textContent, children: entry.subagentPromptElement.children.length }));
      function element(tagName) { return { tagName, className: "", dataset: {}, children: [], textContent: "", append(...values) { this.children.push(...values); }, setAttribute() {} }; }
    JS

    assert_equal "Original delegated prompt", result["text"]
    assert_equal 1, result["children"]
  end

  def test_expanded_subagent_prompt_unclamps_the_same_preview
    stylesheet = File.read(STYLESHEET_PATH)
    assert_includes stylesheet, ".subagent-prompt[open] .subagent-prompt-preview { display: block; overflow: visible;"
    assert_includes stylesheet, "white-space: pre-wrap; -webkit-line-clamp: unset;"
  end

  def test_tool_event_orchestration_clears_assistant_streaming_first
    app = File.read(File.join(ASSETS, "app.js"))
    block = app[/if \(\["tool_execution_start".*?\n  \}/m]
    assert_operator block.index("liveMessageRenderer.clearLiveAssistantStreaming();"), :<, block.index("liveMessageRenderer.renderToolExecutionEvent(event);")
  end

  private

  def module_url(name) = "file://#{File.join(ASSETS, name)}"

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end

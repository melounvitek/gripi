require "minitest/autorun"
require "json"
require "open3"

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

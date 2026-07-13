require "minitest/autorun"
require "json"
require "open3"

class LiveStreamingJsCompactSegmentsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_assistant_tool_call_segments_clear_streaming_before_upsert
    result = run_javascript(<<~JS)
      const { LiveMessageRenderer } = await import(#{module_url("live_message_renderer.js").to_json});
      const calls = [];
      const renderer = Object.create(LiveMessageRenderer.prototype);
      renderer.parser = {
        eventMessage: (event) => event.message,
        contentSegments: () => [{ compact: true, thinking: false, text: "", images: [] }],
        liveEventRole: () => "assistant"
      };
      renderer.conversationController = { resetOversizedFollow() {}, followLiveOutput: () => true };
      renderer.clearLiveAssistantStreaming = () => calls.push("clear");
      renderer.resetLiveAssistantTracking = () => calls.push("reset");
      renderer.upsertLiveAssistantSegment = () => { calls.push("upsert"); return null; };
      renderer.liveAssistantSeen = false;
      const outcome = renderer.renderMessageEvent({ type: "message_update", message: { role: "assistant", content: [] } });
      console.log(JSON.stringify({ calls, outcome }));
    JS

    assert_equal ["clear", "upsert"], result["calls"]
    assert_equal({ "roleName" => "assistant", "assistantEnded" => false, "finalAssistantEnded" => false, "rendered" => true }, result["outcome"])
  end

  private

  def module_url(name) = "file://#{File.join(ASSETS, name)}"

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end

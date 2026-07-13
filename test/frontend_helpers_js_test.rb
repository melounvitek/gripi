require "minitest/autorun"
require "json"
require "open3"

class FrontendHelpersJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_formatting_helpers_are_directly_importable
    results = run_javascript(<<~JS)
      const helpers = await import(#{module_url("formatting.js").to_json});
      console.log(JSON.stringify([
        helpers.compactNumber(1500),
        helpers.formatWaitDuration(125000),
        helpers.imageAttachmentLabel(1),
        helpers.notificationReplyPreview("  a   reply  "),
        helpers.messageFingerprint("toolResult", " Done\\r\\n", "123")
      ]));
    JS

    assert_equal ["1.5k", "2m 05s", "1 image attached", "a reply"], results.first(4)
    assert_match(/\Atool:123:[0-9a-f]+\z/, results.last)
  end

  def test_event_timestamp_prefers_gateway_receipt_time
    results = run_javascript(<<~JS)
      const { eventTimestamp } = await import(#{module_url("formatting.js").to_json});
      console.log(JSON.stringify([
        eventTimestamp({ gatewayTimestamp: 1234, timestamp: "native" }),
        eventTimestamp({ timestamp: "native" })
      ]));
    JS

    assert_equal [1234, "native"], results
  end

  def test_event_polling_is_fast_only_for_visible_running_sessions
    results = run_javascript(<<~JS)
      const { eventPollingDelay } = await import(#{module_url("polling.js").to_json});
      console.log(JSON.stringify([
        eventPollingDelay(false, "running", 0),
        eventPollingDelay(false, "running", 20),
        eventPollingDelay(true, "running", 0),
        eventPollingDelay(false, "done", 0),
        eventPollingDelay(false, "done", 2),
        eventPollingDelay(false, "done", 6),
        eventPollingDelay(false, "running", 0, true),
        eventPollingDelay(true, "running", 0, true)
      ]));
    JS

    assert_equal [250, 250, 10_000, 1_000, 2_000, 5_000, 2_000, 10_000], results
  end

  def test_keyboard_scroll_keys_keep_legacy_spacebar_support
    results = run_javascript(<<~JS)
      const { keyboardScrollKey } = await import(#{module_url("shortcuts.js").to_json});
      console.log(JSON.stringify(["PageDown", " ", "Spacebar", "Enter"].map((key) => keyboardScrollKey({ key }))));
    JS

    assert_equal [true, true, true, false], results
  end

  def test_url_helpers_preserve_query_parameters_without_browser_globals
    results = run_javascript(<<~JS)
      const helpers = await import(#{module_url("urls.js").to_json});
      const location = { href: "https://example.test/?project=demo&session=old", origin: "https://example.test", search: "?project=demo&session=old" };
      console.log(JSON.stringify([
        helpers.sessionUrl("new path", location),
        helpers.sessionFragmentUrl("/?session=next&project=demo", location).href,
        helpers.newSessionModalUrl(undefined, location).href
      ]));
    JS

    assert_equal [
      "/?session=new+path&project=demo",
      "https://example.test/session_fragment?session=next&project=demo",
      "https://example.test/new_session_modal?project=demo&session=old"
    ], results
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

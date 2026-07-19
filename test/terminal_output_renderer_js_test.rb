require "minitest/autorun"
require "json"
require "open3"

class TerminalOutputRendererJsTest < Minitest::Test
  ASSETS = File.expand_path("../public/assets", __dir__)

  def test_detects_terminal_controls_without_routing_plain_or_crlf_text
    result = run_javascript(<<~JS)
      const { hasTerminalControls } = await import(#{module_url("terminal_output_renderer.js").to_json});
      console.log(JSON.stringify({
        plain: hasTerminalControls("one\\ntwo"),
        crlf: hasTerminalControls("one\\r\\ntwo"),
        carriageReturn: hasTerminalControls("10%\\r20%"),
        escape: hasTerminalControls("\\x1b[31mred"),
        backspace: hasTerminalControls("ab\\bc")
      }));
    JS

    assert_equal false, result["plain"]
    assert_equal false, result["crlf"]
    assert_equal true, result["carriageReturn"]
    assert_equal true, result["escape"]
    assert_equal true, result["backspace"]
  end

  def test_renders_carriage_returns_cursor_movement_and_erasure_as_screen_state
    result = render_cases(
      progress: "Progress 10%\rProgress 90%",
      cursor: "abc\e[2DXY",
      erase: "stale text\rnew\e[K"
    )

    assert_equal ["Progress 90%"], result.dig("progress", "lines").map { |line| line["text"] }
    assert_equal ["aXY"], result.dig("cursor", "lines").map { |line| line["text"] }
    assert_equal ["new"], result.dig("erase", "lines").map { |line| line["text"] }
  end

  def test_clear_and_home_replace_previous_screen_snapshots
    result = render_cases(screen: "old one\nold two\e[2J\e[Hnew one\nnew two")

    assert_equal ["new one", "new two"], result.dig("screen", "lines").map { |line| line["text"] }
  end

  def test_preserves_unicode_and_common_ansi_styles
    result = render_cases(styled: "界 \e[1;3;4;31;44mred\e[0m \e[38;2;1;2;3mtrue\e[0m")
    line = result.dig("styled", "lines", 0)
    red = line.fetch("runs").find { |run| run["text"] == "red" }
    true_color = line.fetch("runs").find { |run| run["text"] == "true" }

    assert_equal "界 red true", line["text"]
    assert_equal({ "mode" => "palette", "value" => 1 }, red.dig("style", "foreground"))
    assert_equal({ "mode" => "palette", "value" => 4 }, red.dig("style", "background"))
    assert_equal true, red.dig("style", "bold")
    assert_equal true, red.dig("style", "italic")
    assert_equal true, red.dig("style", "underline")
    assert_equal({ "mode" => "rgb", "value" => 0x010203 }, true_color.dig("style", "foreground"))
  end

  def test_ignores_terminal_links_clipboard_requests_and_titles
    result = render_cases(unsafe: "\e]0;window title\a\e]52;c;Y2xpcGJvYXJk\a\e]8;;javascript:alert(1)\aSafe\e]8;;\a")
    line = result.dig("unsafe", "lines", 0)

    assert_equal "Safe", line["text"]
    run = line.fetch("runs").fetch(0)
    assert_equal ["style", "text"], run.keys.sort
    assert_nil run.dig("style", "link")
  end

  def test_bounds_input_and_terminal_geometry_while_preferring_the_latest_full_screen
    result = run_javascript(<<~JS)
      const { renderTerminalOutput } = await import(#{module_url("terminal_output_renderer.js").to_json});
      const source = `${"x".repeat(200)}\\x1b[2J\\x1b[Hfinal`;
      const rendered = await renderTerminalOutput(source, { maxInputChars: 40, maxColumns: 20, maxRows: 10 });
      console.log(JSON.stringify(rendered));
    JS

    assert_equal true, result["truncated"]
    assert_operator result["columns"], :<=, 20
    assert_operator result["rows"], :<=, 10
    assert_equal "final", result.fetch("lines").last.fetch("text")
  end

  private

  def render_cases(cases)
    run_javascript(<<~JS)
      const { renderTerminalOutput } = await import(#{module_url("terminal_output_renderer.js").to_json});
      const cases = #{JSON.generate(cases)};
      const output = {};
      for (const [name, value] of Object.entries(cases)) output[name] = await renderTerminalOutput(value);
      console.log(JSON.stringify(output));
    JS
  end

  def module_url(name) = "file://#{File.join(ASSETS, name)}"

  def run_javascript(source)
    stdout, stderr, status = Open3.capture3("node", "--input-type=module", "-e", source)
    assert status.success?, stderr
    JSON.parse(stdout)
  end
end

require "minitest/autorun"
require_relative "../lib/pi_bash_execution_text"

class PiBashExecutionTextTest < Minitest::Test
  def test_renders_native_bash_execution_context_text
    cases = [
      [
        { "command" => "printf ok", "output" => "ok", "exitCode" => 0, "cancelled" => false, "truncated" => false },
        "Ran `printf ok`\n```\nok\n```"
      ],
      [
        { "command" => "true", "output" => "", "exitCode" => 0, "cancelled" => false, "truncated" => false },
        "Ran `true`\n(no output)"
      ],
      [
        { "command" => "sleep 30", "output" => "", "cancelled" => true, "truncated" => false },
        "Ran `sleep 30`\n(no output)\n\n(command cancelled)"
      ],
      [
        { "command" => "false", "output" => "failed", "exitCode" => 7, "cancelled" => false, "truncated" => false },
        "Ran `false`\n```\nfailed\n```\n\nCommand exited with code 7"
      ],
      [
        { "command" => "generate", "output" => "tail", "exitCode" => 0, "cancelled" => false, "truncated" => true, "fullOutputPath" => "/tmp/full.log" },
        "Ran `generate`\n```\ntail\n```\n\n[Output truncated. Full output: /tmp/full.log]"
      ]
    ]

    cases.each do |message, expected|
      assert_equal expected, PiBashExecutionText.render(message)
      assert_equal expected.length, PiBashExecutionText.character_length(
        command_characters: message.fetch("command").length,
        output_characters: message.fetch("output").length,
        output_empty: message.fetch("output").empty?,
        exit_code: message["exitCode"],
        cancelled: message["cancelled"] == true,
        truncated: message["truncated"] == true,
        full_output_path_characters: message["fullOutputPath"]&.length
      )
    end
  end
end

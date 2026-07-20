class PiBashExecutionText
  class << self
    def render(message)
      text = "Ran `#{message.fetch("command")}`\n"
      output = message.fetch("output")
      text << if output.empty?
        "(no output)"
      else
        "```\n#{output}\n```"
      end

      if message["cancelled"] == true
        text << "\n\n(command cancelled)"
      elsif message["exitCode"].is_a?(Integer) && !message["exitCode"].zero?
        text << "\n\nCommand exited with code #{message["exitCode"]}"
      end
      if message["truncated"] == true && !message["fullOutputPath"].to_s.empty?
        text << "\n\n[Output truncated. Full output: #{message["fullOutputPath"]}]"
      end
      text
    end

    def character_length(command_characters:, output_characters:, output_empty:, exit_code:, cancelled:, truncated:, full_output_path_characters: nil)
      length = "Ran ``\n".length + command_characters
      length += output_empty ? "(no output)".length : "```\n\n```".length + output_characters
      if cancelled
        length += "\n\n(command cancelled)".length
      elsif exit_code.is_a?(Integer) && !exit_code.zero?
        length += "\n\nCommand exited with code ".length + exit_code.to_s.length
      end
      if truncated && full_output_path_characters
        length += "\n\n[Output truncated. Full output: ]".length + full_output_path_characters
      end
      length
    end
  end
end

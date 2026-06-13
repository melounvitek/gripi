require "json"
require "time"

class PiSessionStore
  Session = Struct.new(
    :path,
    :cwd,
    :id,
    :display_name,
    :first_user_message,
    :message_count,
    :created_at,
    :modified_at,
    keyword_init: true
  )

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :expanded, :error, keyword_init: true)

  def initialize(root: File.expand_path("~/.pi/agent/sessions"))
    @root = root
  end

  def sessions
    Dir.glob(File.join(@root, "**", "*.jsonl")).filter_map { |path| parse_session(path) }
       .sort_by { |session| session.modified_at || Time.at(0) }
       .reverse
  end

  def grouped_sessions
    sessions.group_by(&:cwd)
  end

  def messages(path)
    read_entries(path).filter_map do |entry|
      next unless entry["type"] == "message"

      message = entry["message"] || {}
      role = message["role"]
      text = content_text(message["content"])
      next if role.nil? || text.empty?

      Message.new(
        role: role,
        text: text,
        timestamp: parse_time(entry["timestamp"]),
        compact: compact_message?(message),
        summary: compact_summary(message),
        expanded: message["isError"] == true,
        error: message["isError"] == true
      )
    end
  end

  private

  def parse_session(path)
    session_entry = nil
    latest_name = nil
    first_user_message = nil
    message_count = 0

    read_entries(path).each do |entry|
      case entry["type"]
      when "session"
        session_entry ||= entry
      when "session_info"
        latest_name = entry["name"] unless entry["name"].to_s.empty?
      when "message"
        message = entry["message"] || {}
        message_count += 1 unless message["role"] == "toolResult"
        if first_user_message.nil? && message["role"] == "user"
          first_user_message = content_text(message["content"])
        end
      end
    end

    return unless session_entry

    stat = File.stat(path)
    display_name = latest_name || first_user_message || File.basename(path, ".jsonl")

    Session.new(
      path: path,
      cwd: session_entry["cwd"] || "Unknown cwd",
      id: session_entry["id"],
      display_name: display_name,
      first_user_message: first_user_message,
      message_count: message_count,
      created_at: parse_time(session_entry["timestamp"]) || stat.ctime,
      modified_at: stat.mtime
    )
  end

  def read_entries(path)
    File.readlines(path, chomp: true).filter_map do |line|
      next if line.strip.empty?

      JSON.parse(line)
    rescue JSON::ParserError
      nil
    end
  end

  def content_text(content)
    Array(content).filter_map do |part|
      next part if part.is_a?(String)
      next unless part.is_a?(Hash)

      part["text"] || part["thinking"] || tool_text(part)
    end.join("\n")
  end

  def compact_message?(message)
    return true if message["role"] == "toolResult"

    content = Array(message["content"])
    return false unless message["role"] == "assistant" && content.any?

    content.all? do |part|
      part.is_a?(Hash) && ["thinking", "toolCall", "toolResult"].include?(part["type"])
    end
  end

  def compact_summary(message)
    return message["toolName"] || "tool result" if message["role"] == "toolResult"

    labels = Array(message["content"]).filter_map do |part|
      next unless part.is_a?(Hash)

      case part["type"]
      when "thinking"
        "thinking"
      when "toolCall", "toolResult"
        part["name"] || part["toolName"] || "tool"
      end
    end
    labels.uniq.join(" + ")
  end

  def tool_text(part)
    case part["type"]
    when "toolCall"
      name = part["name"] || "tool"
      arguments = part["arguments"]
      arguments && !arguments.empty? ? "[tool: #{name}]\n#{JSON.pretty_generate(arguments)}" : "[tool: #{name}]"
    when "toolResult"
      part["output"] || part["result"] || "[tool result]"
    end
  end

  def parse_time(value)
    Time.parse(value) if value
  rescue ArgumentError, TypeError
    nil
  end
end

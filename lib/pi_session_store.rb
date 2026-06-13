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

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :expanded, :error, :tool_call_id, :tool_name, :raw_details, keyword_init: true)

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
    pending_tool_calls = {}
    read_entries(path).each_with_object([]) do |entry, rendered_messages|
      next unless entry["type"] == "message"

      messages_from_entry(entry).each do |message|
        if message.role == "toolResult" && message.tool_name == "bash" && pending_tool_calls[message.tool_call_id]
          call_message = pending_tool_calls.delete(message.tool_call_id)
          call_message.text = [call_message.text, message.text].reject(&:empty?).join("\n\n")
          call_message.raw_details = [call_message.raw_details, message.raw_details].compact.reject(&:empty?).join("\n\n")
          call_message.expanded ||= message.expanded
          call_message.error ||= message.error
          next
        end

        rendered_messages << message
        pending_tool_calls[message.tool_call_id] = message if message.tool_name == "bash" && message.tool_call_id
      end
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

  def messages_from_entry(entry)
    message = entry["message"] || {}
    role = message["role"]
    return [] if role.nil?

    if role == "assistant"
      assistant_messages_from_entry(message, parse_time(entry["timestamp"]))
    else
      text = content_text(message["content"])
      return [] if text.empty?

      [Message.new(
        role: role,
        text: text,
        timestamp: parse_time(entry["timestamp"]),
        compact: compact_message?(message),
        summary: compact_summary(message),
        expanded: message["isError"] == true,
        error: message["isError"] == true,
        tool_call_id: message["toolCallId"],
        tool_name: message["toolName"],
        raw_details: compact_raw_details(message["content"]) || (compact_message?(message) ? JSON.pretty_generate(message) : nil)
      )]
    end
  end

  def assistant_messages_from_entry(message, timestamp)
    content_groups(message["content"]).filter_map do |compact, parts|
      text = content_text(parts)
      next if text.empty?

      tool_call = parts.find { |part| part.is_a?(Hash) && part["type"] == "toolCall" }
      tool_name = tool_call && (tool_call["name"] || tool_call["toolName"])
      tool_call_id = tool_call && tool_call["id"]

      Message.new(
        role: "assistant",
        text: text,
        timestamp: timestamp,
        compact: compact,
        summary: compact ? compact_summary(message.merge("content" => parts)) : nil,
        expanded: false,
        error: false,
        tool_call_id: tool_call_id,
        tool_name: tool_name,
        raw_details: compact ? compact_raw_details(parts) : nil
      )
    end
  end

  def content_groups(content)
    groups = []
    Array(content).each do |part|
      compact = compact_part?(part)
      if bash_tool_call?(part)
        groups << [true, [part]]
      elsif compact
        groups << [true, [part]]
      elsif groups.last && groups.last.first == false
        groups.last.last << part
      else
        groups << [false, [part]]
      end
    end
    groups
  end

  def content_text(content)
    Array(content).filter_map do |part|
      next part if part.is_a?(String)
      next unless part.is_a?(Hash)

      part["text"] || thinking_text(part) || tool_text(part)
    end.join("\n")
  end

  def compact_part?(part)
    part.is_a?(Hash) && ["thinking", "toolCall", "toolResult"].include?(part["type"])
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
      next bash_command_line(part) if bash_tool_call?(part)

      case part["type"]
      when "thinking"
        "thinking" unless part["thinking"].to_s.empty?
      when "toolCall", "toolResult"
        part["name"] || part["toolName"] || "tool"
      end
    end
    labels.uniq.join(" + ")
  end

  def compact_raw_details(content)
    details = Array(content).select do |part|
      part.is_a?(Hash) && ["toolCall", "toolResult"].include?(part["type"])
    end
    return nil if details.empty?

    details.map { |part| JSON.pretty_generate(part) }.join("\n\n")
  end

  def thinking_text(part)
    return unless part["type"] == "thinking"

    part["thinking"] unless part["thinking"].to_s.empty?
  end

  def tool_text(part)
    case part["type"]
    when "toolCall"
      return bash_command_line(part) if bash_tool_call?(part)

      name = part["name"] || "tool"
      arguments = part["arguments"]
      arguments && !arguments.empty? ? "[tool: #{name}]\n#{JSON.pretty_generate(arguments)}" : "[tool: #{name}]"
    when "toolResult"
      part["output"] || part["result"] || "[tool result]"
    end
  end

  def bash_tool_call?(part)
    part.is_a?(Hash) && part["type"] == "toolCall" && part["name"] == "bash"
  end

  def bash_command_line(part)
    arguments = part["arguments"].is_a?(Hash) ? part["arguments"] : {}
    command = arguments["command"].to_s
    timeout = arguments["timeout"]
    suffix = timeout ? " (timeout #{timeout}s)" : ""
    "$ #{command}#{suffix}"
  end

  def parse_time(value)
    Time.parse(value) if value
  rescue ArgumentError, TypeError
    nil
  end
end

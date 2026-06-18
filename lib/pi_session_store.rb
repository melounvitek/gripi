require "json"
require "time"
require "erb"

class PiSessionStore
  Session = Struct.new(
    :path,
    :cwd,
    :id,
    :display_name,
    :first_user_message,
    :message_count,
    :assistant_response_count,
    :latest_assistant_response_preview,
    :created_at,
    :modified_at,
    keyword_init: true
  )

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :expanded, :error, :tool_call_id, :tool_name, :raw_details, :thinking, :tool_summary_html, :tool_transcript, keyword_init: true)
  Status = Struct.new(:provider, :model_id, :thinking_level, :context_tokens, :context_limit, :context_percent, :cost_total, keyword_init: true)

  @session_cache = {}
  @session_cache_mutex = Mutex.new

  class << self
    def cached_session(path, signature)
      @session_cache_mutex.synchronize do
        cached = @session_cache[path]
        cached[:session] if cached && cached[:signature] == signature
      end
    end

    def cache_session(path, signature, session)
      @session_cache_mutex.synchronize do
        @session_cache[path] = { signature: signature, session: session }
      end
    end
  end

  def initialize(root: File.expand_path("~/.pi/agent/sessions"), delete_missing_cwds: false)
    @root = root
    @delete_missing_cwds = delete_missing_cwds
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
        if message.role == "toolResult" && pending_tool_calls[message.tool_call_id]
          call_message = pending_tool_calls.delete(message.tool_call_id)
          call_message.text = paired_tool_text(call_message, message)
          call_message.raw_details = [call_message.raw_details, message.raw_details].compact.reject(&:empty?).join("\n\n")
          call_message.expanded ||= message.expanded
          call_message.error ||= message.error
          next
        end

        rendered_messages << message
        pending_tool_calls[message.tool_call_id] = message if message.tool_call_id && pair_tool_result?(message.tool_name)
      end
    end
  end

  def status(path)
    latest = Status.new

    read_entries(path).each do |entry|
      case entry["type"]
      when "model_change"
        latest.provider = entry["provider"] unless entry["provider"].to_s.empty?
        latest.model_id = entry["modelId"] || entry["model"] unless (entry["modelId"] || entry["model"]).to_s.empty?
      when "thinking_level_change"
        latest.thinking_level = entry["thinkingLevel"] || entry["thinking_level"] unless (entry["thinkingLevel"] || entry["thinking_level"]).to_s.empty?
      when "message"
        apply_usage(latest, entry["message"])
      end
    end

    latest
  end

  private

  def apply_usage(status, message)
    return unless message.is_a?(Hash) && message["role"] == "assistant" && message["usage"].is_a?(Hash)

    usage = message["usage"]
    status.context_tokens = usage["totalTokens"] || usage["total_tokens"] || usage["tokens"]
    status.context_limit = usage["contextWindow"] || usage["context_window"] || usage["contextLimit"] || usage["context_limit"]
    status.context_percent = usage["contextPercent"] || usage["context_percent"]
    status.cost_total = usage.dig("cost", "total") || usage["costTotal"] || usage["cost_total"]
  end

  def parse_session(path)
    stat = File.stat(path)
    signature = [stat.size, stat.mtime.to_f]
    cached_session = self.class.cached_session(path, signature)
    if cached_session
      return if delete_session_with_missing_cwd?(path, cached_session.cwd)

      return cached_session
    end

    session_entry = nil
    latest_name = nil
    first_user_message = nil
    message_count = 0
    assistant_response_count = 0
    latest_assistant_response_preview = nil

    read_entries(path).each do |entry|
      case entry["type"]
      when "session"
        session_entry ||= entry
      when "session_info"
        latest_name = entry["name"] unless entry["name"].to_s.empty?
      when "message"
        message = entry["message"] || {}
        message_count += 1 unless message["role"] == "toolResult"
        if final_assistant_response?(message)
          assistant_response_count += 1
          latest_assistant_response_preview = response_preview(final_assistant_response_text(message))
        end
        if first_user_message.nil? && message["role"] == "user"
          first_user_message = content_text(message["content"])
        end
      end
    end

    return unless session_entry
    return if delete_session_with_missing_cwd?(path, session_entry["cwd"])

    display_name = latest_name || first_user_message || File.basename(path, ".jsonl")

    session = Session.new(
      path: path,
      cwd: session_entry["cwd"] || "Unknown cwd",
      id: session_entry["id"],
      display_name: display_name,
      first_user_message: first_user_message,
      message_count: message_count,
      assistant_response_count: assistant_response_count,
      latest_assistant_response_preview: latest_assistant_response_preview,
      created_at: parse_time(session_entry["timestamp"]) || stat.ctime,
      modified_at: stat.mtime
    )
    self.class.cache_session(path, signature, session)
    session
  end

  def final_assistant_response?(message)
    message["role"] == "assistant" && !final_assistant_response_text(message).empty?
  end

  def final_assistant_response_text(message)
    Array(message["content"]).filter_map do |part|
      next part if part.is_a?(String)
      next unless part.is_a?(Hash) && part["type"] == "text"

      part["text"]
    end.join("\n").strip
  end

  def response_preview(text)
    preview = text.to_s.gsub(/<[^>]*>/, " ").gsub(/\bjavascript:/i, "").gsub(/\s+/, " ").strip
    preview.length > 180 ? "#{preview[0, 177]}…" : preview
  end

  def delete_session_with_missing_cwd?(path, cwd)
    return false unless @delete_missing_cwds && !cwd.to_s.empty? && !Dir.exist?(cwd)

    File.delete(path)
    true
  rescue SystemCallError
    false
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
      text = tool_result_text(message)
      return [] if text.empty?

      [Message.new(
        role: role,
        text: text,
        timestamp: parse_time(entry["timestamp"]),
        compact: compact_message?(message),
        summary: compact_summary(message),
        expanded: role == "toolResult" ? false : message["isError"] == true,
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
        raw_details: compact ? compact_raw_details(parts) : nil,
        thinking: parts.length == 1 && thinking_part?(parts.first),
        tool_summary_html: tool_summary_html(tool_call),
        tool_transcript: transcript_tool?(tool_name)
      )
    end
  end

  def tool_result_text(message)
    return message.dig("details", "diff") if message["toolName"] == "edit" && message.dig("details", "diff").to_s != ""

    content_text(message["content"])
  end

  def paired_tool_text(call_message, result_message)
    return result_message.text if transcript_tool?(call_message.tool_name) && !result_message.error

    [call_message.text, result_message.text].reject(&:empty?).join("\n\n")
  end

  def pair_tool_result?(tool_name)
    ["bash", "read", "edit", "write"].include?(tool_name)
  end

  def transcript_tool?(tool_name)
    ["read", "edit", "write"].include?(tool_name)
  end

  def tool_summary_html(tool_call)
    return unless tool_call.is_a?(Hash) && transcript_tool?(tool_call["name"])

    arguments = tool_call["arguments"].is_a?(Hash) ? tool_call["arguments"] : {}
    h_tool_summary(tool_call["name"], arguments["path"], read_range(arguments))
  end

  def h_tool_summary(name, path, range)
    html = %(<span class="tool-command">#{escape_html(name)}</span>)
    html += %( <span class="tool-path">#{escape_html(path)}</span>) unless path.to_s.empty?
    html += %(<span class="tool-range">:#{escape_html(range)}</span>) unless range.to_s.empty?
    html
  end

  def read_range(arguments)
    offset = arguments["offset"]
    limit = arguments["limit"]
    return unless offset && limit

    "#{offset}-#{offset.to_i + limit.to_i - 1}"
  end

  def escape_html(value)
    ERB::Util.html_escape(value.to_s)
  end

  def content_groups(content)
    groups = []
    Array(content).each do |part|
      compact = compact_part?(part)
      if thinking_part?(part)
        groups << [false, [part]]
      elsif bash_tool_call?(part)
        groups << [true, [part]]
      elsif compact
        groups << [true, [part]]
      elsif groups.last && groups.last.first == false && !thinking_part?(groups.last.last.first)
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
    part.is_a?(Hash) && ["toolCall", "toolResult"].include?(part["type"])
  end

  def thinking_part?(part)
    part.is_a?(Hash) && part["type"] == "thinking"
  end

  def compact_message?(message)
    return true if message["role"] == "toolResult"

    content = Array(message["content"])
    return false unless message["role"] == "assistant" && content.any?

    content.all? do |part|
      part.is_a?(Hash) && ["toolCall", "toolResult"].include?(part["type"])
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

    strip_thinking_heading(part["thinking"])
  end

  def strip_thinking_heading(text)
    text = text.to_s
    return if text.empty?

    text.sub(/\A\s*\*\*[^\n*][^\n]*\*\*\s*\n{2,}/, "")
  end

  def tool_text(part)
    case part["type"]
    when "toolCall"
      return bash_command_line(part) if bash_tool_call?(part)
      return transcript_tool_call_text(part) if transcript_tool?(part["name"])

      name = part["name"] || "tool"
      arguments = part["arguments"]
      arguments && !arguments.empty? ? "[tool: #{name}]\n#{JSON.pretty_generate(arguments)}" : "[tool: #{name}]"
    when "toolResult"
      part["output"] || part["result"] || "[tool result]"
    end
  end

  def transcript_tool_call_text(part)
    arguments = part["arguments"].is_a?(Hash) ? part["arguments"] : {}
    summary = [part["name"], arguments["path"]].compact.join(" ")
    summary += ":#{read_range(arguments)}" if part["name"] == "read" && read_range(arguments)
    return summary unless part["name"] == "edit"

    edit_preview = Array(arguments["edits"]).each_with_index.map do |edit, index|
      next unless edit.is_a?(Hash)

      [
        "Edit #{index + 1}",
        preview_text("-", edit["oldText"]),
        preview_text("+", edit["newText"])
      ].compact.join("\n")
    end.compact.join("\n\n")

    [summary, edit_preview].reject(&:empty?).join("\n")
  end

  def preview_text(prefix, text)
    text = text.to_s
    return if text.empty?

    lines = text.lines(chomp: true)
    preview = lines.first(6).map { |line| "#{prefix} #{line}" }
    preview << "#{prefix} …" if lines.length > 6
    preview.join("\n")
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

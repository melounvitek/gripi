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
    :latest_activity_kind,
    :latest_activity_title,
    :latest_activity_preview,
    :parent_session_path,
    :created_at,
    :modified_at,
    keyword_init: true
  )

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :expanded, :error, :tool_call_id, :tool_name, :raw_details, :thinking, :tool_summary_html, :tool_transcript, :tool_preview, :final_assistant_response, :entry_id, :images, keyword_init: true)
  Status = Struct.new(:provider, :model_id, :thinking_level, :context_tokens, :context_limit, :context_percent, :context_estimated, :cost_total, keyword_init: true)

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

  def messages(path, current_leaf_id: nil)
    pending_tool_calls = {}
    session_entries(path, current_leaf_id: current_leaf_id).each_with_object([]) do |entry, rendered_messages|
      if entry["type"] == "compaction"
        rendered_messages << compaction_message_from_entry(entry)
        next
      end
      next unless entry["type"] == "message"

      messages_from_entry(entry).each do |message|
        if message.role == "toolResult" && pending_tool_calls[message.tool_call_id]
          call_message = pending_tool_calls.delete(message.tool_call_id)
          call_message.text = paired_tool_text(call_message, message)
          call_message.raw_details = [call_message.raw_details, message.raw_details].compact.reject(&:empty?).join("\n\n")
          call_message.expanded ||= message.expanded
          call_message.error ||= message.error
          call_message.tool_preview = false unless message.error
          next
        end

        rendered_messages << message
        pending_tool_calls[message.tool_call_id] = message if message.tool_call_id && pair_tool_result?(message.tool_name)
      end
    end
  end

  def tree_entries(path, current_leaf_id: nil)
    persisted_latest_leaf_id = latest_leaf_id(path)
    entries = read_entries(path).select { |entry| tree_node_entry?(entry) }
    ids = entries.filter_map { |entry| entry["id"] }
    children_by_parent = Hash.new { |hash, key| hash[key] = [] }
    roots = []

    entries.each do |entry|
      parent_id = entry["parentId"]
      if parent_id && ids.include?(parent_id)
        children_by_parent[parent_id] << entry
      else
        roots << entry
      end
    end

    flattened = []
    visit_tree_entries(roots, children_by_parent, flattened, 0, current_leaf_id, persisted_latest_leaf_id)
    flattened
  end

  def latest_leaf_id(path)
    read_entries(path).each_with_object({ leaf_id: nil }) do |entry, state|
      state[:leaf_id] = leaf_id_after_entry(entry) if tree_node_entry?(entry)
    end.fetch(:leaf_id)
  end

  def status(path)
    latest = Status.new
    entries = read_entries(path)
    latest_usage_index = nil
    latest_compaction_index = nil

    entries.each_with_index do |entry, index|
      case entry["type"]
      when "model_change"
        latest.provider = entry["provider"] unless entry["provider"].to_s.empty?
        latest.model_id = entry["modelId"] || entry["model"] unless (entry["modelId"] || entry["model"]).to_s.empty?
      when "thinking_level_change"
        latest.thinking_level = entry["thinkingLevel"] || entry["thinking_level"] unless (entry["thinkingLevel"] || entry["thinking_level"]).to_s.empty?
      when "message"
        if apply_usage(latest, entry["message"])
          latest_usage_index = index
        end
      when "compaction"
        latest_compaction_index = index
      end
    end

    if latest_compaction_index && (!latest_usage_index || latest_compaction_index > latest_usage_index)
      apply_compaction_estimate(latest, entries, latest_compaction_index)
    end

    latest
  end

  private

  def session_entries(path, current_leaf_id: nil)
    entries = read_entries(path)
    return entries if current_leaf_id.to_s.empty?

    tree_path = tree_path_entry_ids(entries, current_leaf_id)
    return entries if tree_path.empty?

    entries.select { |entry| !tree_node_entry?(entry) || tree_path.include?(entry["id"]) }
  end

  def tree_path_entry_ids(entries, leaf_id)
    entries_by_id = entries.filter_map { |entry| [entry["id"], entry] if entry["id"] }.to_h
    path = []
    entry = entries_by_id[leaf_id]

    while entry
      path << entry["id"]
      entry = entries_by_id[entry["parentId"]]
    end

    path.reverse
  end

  def tree_node_entry?(entry)
    entry["id"] && entry["type"] != "session"
  end

  def tree_entry_visible?(entry, current_leaf_id)
    return false if ["label", "custom", "model_change", "thinking_level_change", "session_info", "leaf"].include?(entry["type"])
    return false if entry["id"] != current_leaf_id && assistant_tool_call_only_tree_entry?(entry)

    true
  end

  def assistant_tool_call_only_tree_entry?(entry)
    return false unless entry["type"] == "message" && entry.dig("message", "role") == "assistant"

    message = entry["message"] || {}
    return false if assistant_stop_reason_visible?(message["stopReason"])

    !assistant_tree_entry_has_text_content?(message["content"])
  end

  def assistant_tree_entry_has_text_content?(content)
    return content.strip.length.positive? if content.is_a?(String)
    return false unless content.is_a?(Array)

    content.any? do |part|
      part.is_a?(Hash) && part["type"] == "text" && part["text"].to_s.strip.length.positive?
    end
  end

  def assistant_stop_reason_visible?(stop_reason)
    stop_reason && !["stop", "toolUse"].include?(stop_reason)
  end

  def visit_tree_entries(entries, children_by_parent, flattened, depth, current_leaf_id, latest_leaf_id)
    entries.each do |entry|
      flattened << tree_entry_payload(entry, depth, current_leaf_id, latest_leaf_id) if tree_entry_visible?(entry, current_leaf_id)
      visit_tree_entries(children_by_parent[entry["id"]], children_by_parent, flattened, depth + 1, current_leaf_id, latest_leaf_id)
    end
  end

  def tree_entry_payload(entry, depth, current_leaf_id, latest_leaf_id)
    text = tree_entry_text(entry)
    payload = {
      entryId: entry["id"],
      parentId: entry["parentId"],
      depth: depth,
      type: entry["type"],
      role: tree_entry_role(entry),
      text: response_preview(text),
      timestamp: entry["timestamp"],
      current: entry["id"] == current_leaf_id,
      latest: entry["id"] == latest_leaf_id
    }
    payload[:editorText] = text if tree_entry_prefills_editor?(entry)
    payload
  end

  def leaf_id_after_entry(entry)
    entry["type"] == "leaf" ? entry["targetId"] : entry["id"]
  end

  def tree_entry_prefills_editor?(entry)
    entry["type"] == "custom_message" || (entry["type"] == "message" && entry.dig("message", "role") == "user")
  end

  def tree_entry_role(entry)
    case entry["type"]
    when "message"
      entry.dig("message", "role") || "message"
    when "custom_message"
      "custom"
    when "branch_summary"
      "summary"
    when "compaction"
      "compact"
    else
      entry["type"]
    end
  end

  def tree_entry_text(entry)
    case entry["type"]
    when "message"
      content_text(entry.dig("message", "content"))
    when "custom_message"
      content_text(entry["content"])
    when "branch_summary", "compaction"
      entry["summary"]
    else
      ""
    end
  end

  def apply_usage(status, message)
    return false unless message.is_a?(Hash) && message["role"] == "assistant" && message["usage"].is_a?(Hash)

    usage = message["usage"]
    status.context_tokens = usage["totalTokens"] || usage["total_tokens"] || usage["tokens"]
    status.context_limit = usage["contextWindow"] || usage["context_window"] || usage["contextLimit"] || usage["context_limit"]
    status.context_percent = usage["contextPercent"] || usage["context_percent"]
    status.context_estimated = false
    status.cost_total = usage.dig("cost", "total") || usage["costTotal"] || usage["cost_total"]
    true
  end

  def apply_compaction_estimate(status, entries, compaction_index)
    compaction = entries[compaction_index]
    tokens = estimate_compacted_context_tokens(entries, compaction_index, compaction)
    return unless tokens && tokens.positive?

    status.context_tokens = tokens
    status.context_limit = nil
    status.context_percent = nil
    status.context_estimated = true
  end

  def estimate_compacted_context_tokens(entries, compaction_index, compaction)
    first_kept_entry_id = compaction["firstKeptEntryId"].to_s
    first_kept_index = first_kept_entry_id.empty? ? nil : entries.find_index { |entry| entry["id"] == first_kept_entry_id }
    kept_entries = first_kept_index ? entries[first_kept_index...compaction_index] : []
    text = [compaction["summary"], *kept_entries.map { |entry| estimate_text_from_entry(entry) }].compact.join("\n")
    estimate_tokens(text)
  end

  def estimate_text_from_entry(entry)
    return unless entry["type"] == "message"

    content_text(entry.dig("message", "content"))
  end

  def estimate_tokens(text)
    text = text.to_s
    return 0 if text.empty?

    (text.length / 4.0).ceil
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
    latest_activity_kind = nil
    latest_activity_title = nil
    latest_activity_preview = nil

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
          latest_activity_kind = "assistant"
          latest_activity_title = nil
          latest_activity_preview = latest_assistant_response_preview
        end
        if first_user_message.nil? && message["role"] == "user"
          first_user_message = content_text(message["content"])
        end
      when "compaction"
        latest_activity_kind = "compaction"
        latest_activity_title = "Conversation compacted"
        latest_activity_preview = response_preview(entry["summary"])
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
      latest_activity_kind: latest_activity_kind,
      latest_activity_title: latest_activity_title,
      latest_activity_preview: latest_activity_preview,
      parent_session_path: session_entry["parentSession"],
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
    final_assistant_response_text_from_parts(Array(message["content"]))
  end

  def final_assistant_response_parts?(parts)
    !final_assistant_response_text_from_parts(parts).empty?
  end

  def final_assistant_response_text_from_parts(parts)
    parts.filter_map do |part|
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

  def compaction_message_from_entry(entry)
    summary = entry["summary"].to_s.strip
    text = summary.empty? ? JSON.pretty_generate(entry) : summary
    Message.new(
      role: "status",
      text: text,
      timestamp: parse_time(entry["timestamp"]),
      compact: true,
      summary: "Conversation compacted",
      expanded: false,
      raw_details: JSON.pretty_generate(entry)
    )
  end

  def messages_from_entry(entry)
    message = entry["message"] || {}
    role = message["role"]
    return [] if role.nil?

    if role == "assistant"
      assistant_messages_from_entry(message, parse_time(entry["timestamp"]))
    else
      text = tool_result_text(message)
      images = content_images(message["content"])
      return [] if text.empty? && images.empty?

      [Message.new(
        role: role,
        text: text,
        timestamp: parse_time(entry["timestamp"]),
        entry_id: entry["id"],
        compact: compact_message?(message),
        summary: compact_summary(message),
        expanded: role == "toolResult" ? false : message["isError"] == true,
        error: message["isError"] == true,
        tool_call_id: message["toolCallId"],
        tool_name: message["toolName"],
        raw_details: compact_raw_details(message["content"]) || (compact_message?(message) ? JSON.pretty_generate(message) : nil),
        images: images,
        tool_transcript: transcript_tool?(message["toolName"])
      )]
    end
  end

  def assistant_messages_from_entry(message, timestamp)
    content_groups(message["content"]).filter_map do |compact, parts|
      text = content_text(parts).to_s
      next if text.empty? && !compact

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
        tool_transcript: transcript_tool?(tool_name),
        tool_preview: tool_call && tool_name == "edit",
        final_assistant_response: final_assistant_response_parts?(parts)
      )
    end
  end

  def tool_result_text(message)
    return message.dig("details", "diff") if message["toolName"] == "edit" && message.dig("details", "diff").to_s != ""

    content_text(message["content"])
  end

  def paired_tool_text(call_message, result_message)
    return result_message.text if call_message.tool_name == "bash"

    if transcript_tool?(call_message.tool_name) && !result_message.error
      return [call_message.text, result_message.text].reject(&:empty?).join("\n\n") if call_message.tool_name == "write"

      return result_message.text
    end

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

  def content_images(content)
    Array(content).filter_map do |part|
      next unless part.is_a?(Hash) && part["type"] == "image"
      next unless ["image/png", "image/jpeg", "image/gif", "image/webp"].include?(part["mimeType"])
      next if part["data"].to_s.empty?

      { data: part["data"], mime_type: part["mimeType"] }
    end
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
      return "" if bash_tool_call?(part)
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
    return preview_text("+", arguments["content"]).to_s if part["name"] == "write"
    return "" unless part["name"] == "edit"

    edit_preview = Array(arguments["edits"]).each_with_index.map do |edit, index|
      next unless edit.is_a?(Hash)

      [
        "Edit #{index + 1}",
        preview_text("-", edit["oldText"]),
        preview_text("+", edit["newText"])
      ].compact.join("\n")
    end.compact.join("\n\n")

    edit_preview
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

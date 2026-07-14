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
    :conversation_activity_at,
    keyword_init: true
  )

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :error, :tool_call_id, :tool_name, :thinking, :tool_summary_html, :tool_transcript, :tool_preview, :tool_prompt, :final_assistant_response, :entry_id, :images, keyword_init: true)
  Status = Struct.new(:provider, :model_id, :thinking_level, :context_tokens, :context_limit, :context_percent, :context_estimated, :cost_total, keyword_init: true)
  Conversation = Struct.new(:messages, :latest_leaf_id, :status, keyword_init: true)
  FileSnapshot = Struct.new(:device, :inode, :size, :mtime_ns, :append_cursor, :persisted_leaf_id, :complete, keyword_init: true) do
    def revision
      [device, inode, size, mtime_ns, append_cursor, persisted_leaf_id, complete ? 1 : 0].join(":")
    end
  end

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

  def initialize(root: File.expand_path("~/.pi/agent/sessions"), hide_missing_cwds: false)
    @root = root
    @hide_missing_cwds = hide_missing_cwds
  end

  def sessions
    Dir.glob(File.join(@root, "**", "*.jsonl")).filter_map { |path| parse_session(path) }
       .sort_by { |session| session.conversation_activity_at || Time.at(0) }
       .reverse
  end

  def grouped_sessions
    sessions.group_by(&:cwd)
  end

  def conversation(path, current_leaf_id: nil)
    entries = read_entries(path)
    Conversation.new(
      messages: messages_from_entries(session_entries(entries, current_leaf_id: current_leaf_id)),
      latest_leaf_id: latest_leaf_id_from_entries(entries),
      status: status_from_entries(entries)
    )
  end

  def messages(path, current_leaf_id: nil)
    entries = read_entries(path)
    messages_from_entries(session_entries(entries, current_leaf_id: current_leaf_id))
  end

  def tool_call_timestamps(path, tool_call_ids)
    subagent_tool_call_context(path, tool_call_ids).filter_map do |tool_call_id, details|
      [tool_call_id, details[:timestamp]] if details[:timestamp]
    end.to_h
  end

  def subagent_tool_call_context(path, tool_call_ids)
    requested_ids = Array(tool_call_ids).to_h { |tool_call_id| [tool_call_id, true] }
    return {} if requested_ids.empty?

    subagent_tool_calls_from_entries(read_entries(path))
      .select { |tool_call_id, _details| requested_ids[tool_call_id] }
      .transform_values do |details|
        { timestamp: details[:timestamp]&.utc&.iso8601(3), prompt: details[:prompt] }
      end
  rescue SystemCallError
    {}
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
    latest_leaf_id_from_entries(read_entries(path))
  end

  def file_snapshot(path)
    3.times do
      File.open(path, "rb") do |file|
        before = file.stat
        append_cursor, persisted_leaf_id, complete = last_append_cursor(file)
        after = file.stat
        current = File.stat(path)
        next unless stable_file_stat?(before, after) && stable_file_stat?(after, current)

        return FileSnapshot.new(
          device: after.dev,
          inode: after.ino,
          size: after.size,
          mtime_ns: stat_mtime_ns(after),
          append_cursor: append_cursor,
          persisted_leaf_id: persisted_leaf_id,
          complete: complete
        )
      end
    end
    raise Errno::EAGAIN, "Session file kept changing while it was read: #{path}"
  end

  def appended_entry_ids(path, previous_snapshot, current_snapshot)
    length = current_snapshot.size - previous_snapshot.size
    return [] unless length.positive?

    File.open(path, "rb") do |file|
      stat = file.stat
      unless stat.dev == current_snapshot.device && stat.ino == current_snapshot.inode && stat.size >= current_snapshot.size
        raise Errno::EAGAIN, "Session file changed while appended entries were read: #{path}"
      end

      file.seek(previous_snapshot.size)
      file.read(length).lines.filter_map do |line|
        next if line.strip.empty?

        JSON.parse(line)["id"]
      end
    end
  end

  def status(path)
    status_from_entries(read_entries(path))
  end

  private

  def messages_from_entries(entries)
    subagent_tool_calls = subagent_tool_calls_from_entries(entries)
    pending_tool_calls = {}
    entries.each_with_object([]) do |entry, rendered_messages|
      if entry["type"] == "compaction"
        rendered_messages << compaction_message_from_entry(entry)
        next
      end

      if (error_message = error_message_from_entry(entry))
        rendered_messages << error_message
        next
      end

      next unless entry["type"] == "message"

      messages_from_entry(entry).each do |message|
        if message.role == "toolResult" && message.tool_name == "subagent" && (tool_call = subagent_tool_calls[message.tool_call_id])
          message.timestamp = tool_call[:timestamp] if tool_call[:timestamp]
          message.tool_prompt = tool_call[:prompt] unless tool_call[:prompt].to_s.empty?
        end

        if message.role == "toolResult" && pending_tool_calls[message.tool_call_id]
          call_message = pending_tool_calls.delete(message.tool_call_id)
          call_message.text = paired_tool_text(call_message, message)
          call_message.error ||= message.error
          call_message.tool_preview = false unless message.error
          call_message.images = [*call_message.images, *message.images]
          next
        end

        rendered_messages << message
        pending_tool_calls[message.tool_call_id] = message if message.tool_call_id && pair_tool_result?(message.tool_name)
      end
    end
  end

  def latest_leaf_id_from_entries(entries)
    entries.each_with_object({ leaf_id: nil }) do |entry, state|
      state[:leaf_id] = leaf_id_after_entry(entry) if tree_node_entry?(entry)
    end.fetch(:leaf_id)
  end

  def status_from_entries(entries)
    latest = Status.new
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
        latest_usage_index = index if apply_usage(latest, entry["message"])
      when "compaction"
        latest_compaction_index = index
      end
    end

    if latest_compaction_index && (!latest_usage_index || latest_compaction_index > latest_usage_index)
      apply_compaction_estimate(latest, entries, latest_compaction_index)
    end

    latest
  end

  def subagent_tool_calls_from_entries(entries)
    entries.each_with_object({}) do |entry, tool_calls|
      message = entry["message"]
      next unless entry["type"] == "message" && message.is_a?(Hash) && message["role"] == "assistant"

      Array(message["content"]).each do |part|
        next unless part.is_a?(Hash) && part["type"] == "toolCall" && part["name"] == "subagent"
        next unless part["id"].is_a?(String) && !part["id"].empty?

        tool_calls[part["id"]] ||= {
          timestamp: parse_iso8601_time(entry["timestamp"]),
          prompt: subagent_prompt(part["arguments"])
        }
      end
    end
  end

  def subagent_prompt(value)
    return unless value.is_a?(Hash)

    task = value["task"]
    return task if task.is_a?(String) && !task.empty?

    items = value["tasks"] || value["chain"] || value["results"]
    prompts = Array(items).filter_map do |item|
      next unless item.is_a?(Hash) && item["task"].is_a?(String) && !item["task"].empty?

      item["agent"].to_s.empty? ? item["task"] : "#{item["agent"]}: #{item["task"]}"
    end
    prompts.join("\n\n") unless prompts.empty?
  end

  def session_entries(entries, current_leaf_id: nil)
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
      return if hide_session_with_missing_cwd?(cached_session.cwd)

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
    conversation_activity_at = nil

    read_entries(path).each do |entry|
      case entry["type"]
      when "session"
        session_entry ||= entry
      when "session_info"
        latest_name = entry["name"] unless entry["name"].to_s.empty?
      when "message"
        message = entry["message"] || {}
        message_count += 1 unless message["role"] == "toolResult"
        if conversation_activity_message?(message)
          entry_time = parse_time(entry["timestamp"])
          conversation_activity_at = entry_time if entry_time && (!conversation_activity_at || entry_time > conversation_activity_at)
        end
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

    display_name = latest_name || first_user_message || File.basename(path, ".jsonl")
    conversation_activity_at ||= parse_time(session_entry["timestamp"])

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
      modified_at: stat.mtime,
      conversation_activity_at: conversation_activity_at
    )
    self.class.cache_session(path, signature, session)
    return if hide_session_with_missing_cwd?(session.cwd)

    session
  end

  def conversation_activity_message?(message)
    return true if message["role"] == "user"
    return false unless message["role"] == "assistant"
    return false unless [nil, "stop", "length"].include?(message["stopReason"])

    !final_assistant_response_text(message).empty?
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
      next if assistant_text_phase(part) == "commentary"

      part["text"]
    end.join("\n").strip
  end

  def assistant_text_phase(part)
    signature = part["textSignature"]
    return unless signature.is_a?(String) && signature.start_with?("{")

    parsed = JSON.parse(signature)
    return unless parsed.is_a?(Hash) && parsed["v"] == 1 && parsed["id"].is_a?(String)

    phase = parsed["phase"]
    phase if %w[commentary final_answer].include?(phase)
  rescue JSON::ParserError
    nil
  end

  def response_preview(text)
    preview = text.to_s.gsub(/<[^>]*>/, " ").gsub(/\bjavascript:/i, "").gsub(/\s+/, " ").strip
    preview.length > 180 ? "#{preview[0, 177]}…" : preview
  end

  def hide_session_with_missing_cwd?(cwd)
    @hide_missing_cwds && !cwd.to_s.empty? && !Dir.exist?(cwd)
  end

  def read_entries(path)
    File.readlines(path, chomp: true).filter_map do |line|
      next if line.strip.empty?

      JSON.parse(line)
    rescue JSON::ParserError
      nil
    end
  end

  def last_append_cursor(file)
    complete = true
    fragments = []
    position = file.size

    while position.positive?
      bytes = [8 * 1024, position].min
      position -= bytes
      file.seek(position)
      chunk = file.read(bytes)
      line_end = chunk.bytesize

      while line_end.positive? && (newline = chunk.rindex("\n", line_end - 1))
        fragment = chunk.byteslice(newline + 1, line_end - newline - 1)
        line = fragments.empty? ? fragment : fragment + fragments.reverse.join
        fragments.clear
        result, valid = append_cursor_from_line(line)
        complete &&= valid
        return [result.fetch(:cursor), result.fetch(:leaf), complete] if result
        line_end = newline
      end

      prefix = chunk.byteslice(0, line_end)
      fragments << prefix unless prefix.empty?
    end

    line = fragments.reverse.join
    result, valid = append_cursor_from_line(line)
    complete &&= valid
    return [result.fetch(:cursor), result.fetch(:leaf), complete] if result

    [nil, nil, complete]
  end

  def append_cursor_from_line(line)
    return [nil, true] if line.strip.empty?

    entry = JSON.parse(line)
    return [nil, true] unless tree_node_entry?(entry)

    [{ cursor: entry["id"], leaf: leaf_id_after_entry(entry) }, true]
  rescue JSON::ParserError
    [nil, false]
  end

  def stable_file_stat?(left, right)
    left.dev == right.dev && left.ino == right.ino && left.size == right.size && stat_mtime_ns(left) == stat_mtime_ns(right)
  end

  def stat_mtime_ns(stat)
    (stat.mtime.to_i * 1_000_000_000) + stat.mtime.nsec
  end

  def compaction_message_from_entry(entry)
    summary = entry["summary"].to_s.strip
    text = summary.empty? ? JSON.pretty_generate(entry) : summary
    Message.new(
      role: "status",
      text: text,
      timestamp: parse_time(entry["timestamp"]),
      compact: true,
      summary: "Conversation compacted"
    )
  end

  def error_message_from_entry(entry)
    return if entry["type"] != "error" && !entry.key?("error") && !entry.key?("finalError")

    text = error_text(entry)
    return if text.empty?

    Message.new(
      role: "error",
      text: text,
      timestamp: parse_time(entry["timestamp"]),
      compact: false,
      error: true
    )
  end

  def error_text(value)
    case value
    when String
      value.strip
    when Hash
      [value["error"], value["finalError"], value["message"], value["text"], value.dig("details", "error"), value.dig("details", "message")].each do |candidate|
        text = error_text(candidate)
        return text unless text.empty?
      end
      ""
    else
      ""
    end
  end

  def messages_from_entry(entry)
    message = entry["message"] || {}
    role = message["role"]
    return [] if role.nil?

    if role == "assistant"
      assistant_messages_from_entry(message, parse_time(entry["timestamp"]))
    else
      general_subagent = general_subagent_details?(message)
      text = general_subagent ? general_subagent_text(message) : tool_result_text(message)
      images = content_images(message["content"])
      return [] if text.empty? && images.empty?

      [Message.new(
        role: role,
        text: text,
        timestamp: parse_time(entry["timestamp"]),
        entry_id: entry["id"],
        compact: compact_message?(message),
        summary: general_subagent ? "subagent general" : compact_summary(message),
        error: message["isError"] == true,
        tool_call_id: message["toolCallId"],
        tool_name: message["toolName"],
        images: images,
        tool_prompt: message["toolName"] == "subagent" ? subagent_prompt(message["details"]) : nil,
        tool_transcript: general_subagent || transcript_tool?(message["toolName"])
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
        error: false,
        tool_call_id: tool_call_id,
        tool_name: tool_name,
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

  def general_subagent_details?(message)
    details = message["details"]
    message["toolName"] == "subagent" && details.is_a?(Hash) && details["tools"].is_a?(Array) && details["usage"].is_a?(Hash)
  end

  def general_subagent_text(message)
    details = message["details"]
    lines = ["#{general_subagent_status_icon(details["status"])} general"]

    details["tools"].each do |tool|
      next unless tool.is_a?(Hash)

      lines << "#{general_subagent_status_icon(tool["status"])} #{general_subagent_tool_call(tool)}"
      output = tool["output"].to_s.strip
      lines.concat(output.lines(chomp: true).map { |line| "  #{line}" }) unless output.empty?
    end

    final_text = details["streamingText"].to_s
    final_text = details["textItems"].last.to_s if final_text.empty? && details["textItems"].is_a?(Array)
    final_text = content_text(message["content"]) if final_text.empty?
    lines.concat(["", final_text]) unless final_text.empty?

    usage = general_subagent_usage_text(details["usage"], details["model"])
    lines.concat(["", usage]) unless usage.empty?
    lines.join("\n")
  end

  def general_subagent_status_icon(status)
    return "✓" if status == "done"
    return "✗" if status == "error"

    "⏳"
  end

  def general_subagent_tool_call(tool)
    arguments = tool["args"].is_a?(Hash) ? tool["args"] : {}
    name = tool["name"].to_s
    path = arguments["path"] || arguments["file_path"]

    case name
    when "bash"
      "$ #{arguments["command"] || "..."}"
    when "read"
      offset = integer_tool_argument(arguments["offset"])
      limit = integer_tool_argument(arguments["limit"])
      range = offset || limit ? ":#{offset || 1}#{limit ? "-#{(offset || 1) + limit - 1}" : ""}" : ""
      "read #{path || "..."}#{range}"
    when "write", "edit"
      "#{name} #{path || "..."}"
    when "grep"
      "grep /#{arguments["pattern"]}/ in #{arguments["path"] || "."}"
    when "find"
      "find #{arguments["pattern"] || "*"} in #{arguments["path"] || "."}"
    when "ls"
      "ls #{arguments["path"] || "."}"
    else
      serialized = safe_json_generate(arguments)
      "#{name.empty? ? "tool" : name} #{serialized.length > 100 ? "#{serialized[0, 100]}…" : serialized}"
    end
  end

  def general_subagent_usage_text(usage, model)
    values = Hash.new(0.0).merge(usage.transform_values { |value| numeric_usage_value(value) })
    parts = []
    turns = values["turns"].to_i
    parts << "#{turns} turn#{turns == 1 ? "" : "s"}" if turns.positive?
    parts << "↑#{compact_usage_number(values["input"])}" if values["input"].positive?
    parts << "↓#{compact_usage_number(values["output"])}" if values["output"].positive?
    parts << "R#{compact_usage_number(values["cacheRead"])}" if values["cacheRead"].positive?
    parts << "W#{compact_usage_number(values["cacheWrite"])}" if values["cacheWrite"].positive?
    parts << format("$%.4f", values["cost"]) if values["cost"].positive?
    parts << "ctx:#{compact_usage_number(values["contextTokens"])}" if values["contextTokens"].positive?
    parts << model if !model.to_s.empty?
    parts.join(" ")
  end

  def integer_tool_argument(value)
    Integer(value) unless value.nil?
  rescue ArgumentError, TypeError, FloatDomainError
    nil
  end

  def safe_json_generate(value)
    JSON.generate(value)
  rescue JSON::GeneratorError
    value.inspect
  end

  def numeric_usage_value(value)
    number = Float(value || 0)
    number.finite? ? number : 0.0
  rescue ArgumentError, TypeError
    0.0
  end

  def compact_usage_number(value)
    number = value.to_f
    return number.round.to_s if number < 1_000
    return format("%.1fk", number / 1_000) if number < 10_000
    return "#{(number / 1_000).round}k" if number < 1_000_000

    format("%.1fM", number / 1_000_000)
  end

  def paired_tool_text(call_message, result_message)
    return result_message.text if call_message.tool_name == "bash"

    if transcript_tool?(call_message.tool_name) && !result_message.error
      return "" if call_message.tool_name == "read"
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
    html += %( <span class="tool-path">#{escape_html(display_home_path(path))}</span>) unless path.to_s.empty?
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
      next if subagent_tool_call?(part)

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

  def subagent_tool_call?(part)
    part.is_a?(Hash) && part["type"] == "toolCall" && part["name"] == "subagent"
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
    command = display_home_path(arguments["command"])
    timeout = arguments["timeout"]
    suffix = timeout ? " (timeout #{timeout}s)" : ""
    "$ #{command}#{suffix}"
  end

  def display_home_path(text)
    home = Dir.home
    text.to_s.gsub(/(?<![A-Za-z0-9_.~\/-])#{Regexp.escape(home)}(?=\/|\z|[^A-Za-z0-9_.~\/-])/, "~")
  end

  def parse_iso8601_time(value)
    Time.iso8601(value) if value.is_a?(String)
  rescue ArgumentError
    nil
  end

  def parse_time(value)
    Time.parse(value) if value
  rescue ArgumentError, TypeError
    nil
  end
end

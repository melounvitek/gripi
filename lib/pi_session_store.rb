require "json"
require "time"
require "erb"
require_relative "pi_session_index"
require_relative "pi_bash_execution_text"

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

  Message = Struct.new(:role, :text, :timestamp, :compact, :summary, :error, :tool_call_id, :tool_name, :thinking, :tool_summary_html, :tool_transcript, :tool_preview, :tool_prompt, :final_assistant_response, :entry_id, :images, :custom_type, :compaction, :bash_exit_code, :bash_cancelled, :bash_truncated, :bash_excluded_from_context, :bash_full_output_path, :bash_recorded_at, keyword_init: true)
  Status = Struct.new(:provider, :model_id, :thinking_level, :context_tokens, :context_limit, :context_percent, :context_estimated, :cost_total, keyword_init: true)
  Conversation = Struct.new(:messages, :latest_stable_tree_position_id, :current_stable_tree_position_id, :status, :subagent_tool_call_context, keyword_init: true)
  ConversationWindow = Struct.new(:messages, :start_index, :total_message_count, :tree_leaf_id, :latest_stable_tree_position_id, :current_stable_tree_position_id, :status, :subagent_tool_call_context, keyword_init: true)
  ProjectedUnit = Struct.new(:key, :entry_ordinal, :dependencies, :tool_name, :minimum_window_bytes, keyword_init: true)
  MAX_SESSION_CACHE_ENTRIES = 10_000
  MAX_SESSION_INDEX_CACHE_BYTES = 32 * 1024 * 1024
  MAX_SESSION_INDEX_CACHE_ENTRIES = 1_000
  CONVERSATION_WINDOW_MIN_MESSAGES = 20
  CONVERSATION_WINDOW_MAX_MESSAGES = 150
  CONVERSATION_WINDOW_BYTE_BUDGET = 128 * 1024
  # Unknown layouts fall back to JSON parsing; only Pi's native key order takes this metadata fast path.
  NATIVE_TOOL_RESULT_PREFIX = /\A\{"type":"message","id":"[^"]+","parentId":(?:null|"[^"]+"),"timestamp":"[^"]+","message":\{"role":"toolResult"/.freeze
  NATIVE_USER_MESSAGE_PREFIX = /\A\{"type":"message","id":"[^"]+","parentId":(?:null|"[^"]+"),"timestamp":"([^"]+)","message":\{"role":"user"/.freeze

  FileSnapshot = Struct.new(:device, :inode, :size, :mtime_ns, :append_cursor, :persisted_leaf_id, :complete, keyword_init: true) do
    def revision
      [device, inode, size, mtime_ns, append_cursor, persisted_leaf_id, complete ? 1 : 0].join(":")
    end
  end

  @session_cache = {}
  @session_cache_mutex = Mutex.new
  @session_cache_clock = 0
  @session_index_cache = {}
  @session_index_cache_mutex = Mutex.new
  @session_index_cache_clock = 0
  @session_index_cache_bytes = 0

  class << self
    def fetch_session(path)
      state = @session_cache_mutex.synchronize do
        @session_cache_clock += 1
        state = @session_cache[path] ||= { lock: Mutex.new, users: 0 }
        state[:users] += 1
        state[:last_used] = @session_cache_clock
        state
      end
      state.fetch(:lock).synchronize do
        stat = File.stat(path)
        signature = [stat.size, stat.mtime.to_f]
        return state[:session] if state.key?(:session) && state[:signature] == signature

        session = yield(stat)
        state[:signature] = signature
        state[:session] = session
      end
    ensure
      if state
        @session_cache_mutex.synchronize do
          state[:users] -= 1
          excess = @session_cache.length - MAX_SESSION_CACHE_ENTRIES
          if excess.positive?
            @session_cache.select { |_cached_path, cached| cached[:users].zero? }
                          .min_by(excess) { |_cached_path, cached| cached[:last_used] }
                          .each { |cached_path, _cached| @session_cache.delete(cached_path) }
          end
        end
      end
    end

    def fetch_session_index(path)
      state = @session_index_cache_mutex.synchronize do
        @session_index_cache_clock += 1
        state = @session_index_cache[path] ||= { lock: Mutex.new, users: 0, bytes: 0 }
        state[:users] += 1
        state[:last_used] = @session_index_cache_clock
        state
      end
      state.fetch(:lock).synchronize do
        return state[:index] if state[:index]&.valid_for_current_path?

        index = yield(state[:index])
        @session_index_cache_mutex.synchronize do
          @session_index_cache_bytes -= state[:bytes]
          state[:bytes] = index.estimated_bytes
          @session_index_cache_bytes += state[:bytes]
        end
        state[:index] = index
      end
    ensure
      if state
        @session_index_cache_mutex.synchronize do
          state[:users] -= 1
          while @session_index_cache_bytes > MAX_SESSION_INDEX_CACHE_BYTES || @session_index_cache.length > MAX_SESSION_INDEX_CACHE_ENTRIES
            cached_path, cached = @session_index_cache.reject { |_path, candidate| candidate[:users].positive? }
                                                              .min_by { |_path, candidate| candidate[:last_used] }
            break unless cached_path

            @session_index_cache_bytes -= cached[:bytes]
            @session_index_cache.delete(cached_path)
          end
        end
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
      latest_stable_tree_position_id: stable_tree_position_id(entries, latest_leaf_id_from_entries(entries)),
      current_stable_tree_position_id: stable_tree_position_id(entries, current_leaf_id),
      status: status_from_entries(entries),
      subagent_tool_call_context: normalized_subagent_tool_call_context(subagent_tool_calls_from_entries(entries))
    )
  end

  def messages(path, current_leaf_id: nil)
    entries = read_entries(path)
    messages_from_entries(session_entries(entries, current_leaf_id: current_leaf_id))
  end

  def conversation_window(path, current_leaf_id: nil, current_leaf_supplied: !current_leaf_id.nil?, cursor: nil, after_cursor: nil, active_tool_call_ids: [])
    index = session_index(path)
    return unless index.supported

    effective_leaf_id = current_leaf_supplied ? current_leaf_id : index.latest_leaf_id
    branch_entries = index.entries_for_leaf(effective_leaf_id, supplied: true)
    return unless branch_entries

    units, subagent_sources = projected_units(branch_entries)
    return unless units

    cursor = [[cursor.nil? ? units.length : cursor.to_i, 0].max, units.length].min
    messages, start_index = render_window(path, index, units, cursor, after_cursor)
    status = status_from_index(index)
    subagent_context = indexed_subagent_tool_call_context(path, index, subagent_sources, active_tool_call_ids)
    raise Errno::EAGAIN unless index.valid_for_current_path?

    ConversationWindow.new(
      messages: messages,
      start_index: start_index,
      total_message_count: units.length,
      tree_leaf_id: effective_leaf_id,
      latest_stable_tree_position_id: stable_tree_position_id_from_index(index, index.latest_leaf_id),
      current_stable_tree_position_id: stable_tree_position_id_from_index(index, current_leaf_id),
      status: status,
      subagent_tool_call_context: subagent_context
    )
  rescue JSON::ParserError, Errno::EAGAIN
    nil
  end

  def tool_call_timestamps(path, tool_call_ids)
    subagent_tool_call_context(path, tool_call_ids).filter_map do |tool_call_id, details|
      [tool_call_id, details[:timestamp]] if details[:timestamp]
    end.to_h
  end

  def subagent_tool_call_context(path, tool_call_ids)
    requested_ids = Array(tool_call_ids).to_h { |tool_call_id| [tool_call_id, true] }
    return {} if requested_ids.empty?

    tool_calls = subagent_tool_calls_from_entries(read_entries(path))
    normalized_subagent_tool_call_context(tool_calls.select { |tool_call_id, _details| requested_ids[tool_call_id] })
  rescue SystemCallError
    {}
  end

  def latest_stable_tree_position_id(path)
    entries = read_entries(path)
    stable_tree_position_id(entries, latest_leaf_id_from_entries(entries))
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

  def cwd_for_session(path)
    expanded_path = File.expand_path(path)
    expanded_root = File.expand_path(@root)
    return unless expanded_path.start_with?("#{expanded_root}#{File::SEPARATOR}") && File.extname(expanded_path) == ".jsonl"

    real_root = File.realpath(expanded_root)
    real_path = File.realpath(expanded_path)
    return unless real_path.start_with?("#{real_root}#{File::SEPARATOR}")

    File.foreach(real_path) do |line|
      next if line.strip.empty?

      entry = JSON.parse(line)
      next unless entry["type"] == "session"

      cwd = entry["cwd"]
      return cwd if cwd.is_a?(String) && !cwd.empty? && File.absolute_path(cwd) == cwd
      return nil
    rescue JSON::ParserError
      next
    end
    nil
  rescue ArgumentError, SystemCallError
    nil
  end

  private

  def session_index(path)
    self.class.fetch_session_index(path) do |previous|
      PiSessionIndex.build(path, previous: previous) { |entry| index_metadata_from_entry(entry) }
    end
  end

  def index_metadata_from_entry(entry)
    messages = unpaired_messages_from_entry(entry)
    {
      type: entry["type"],
      id: entry["id"],
      parent_id: entry["parentId"],
      target_id: entry["targetId"],
      role: entry.dig("message", "role"),
      segments: messages.map do |message|
        PiSessionIndex::Segment.new(role: message.role, tool_call_id: message.tool_call_id, tool_name: message.tool_name, minimum_window_bytes: nil, paired_minimum_window_bytes: nil)
      end,
      subagent_tool_call_ids: subagent_tool_calls_from_entries([entry]).keys,
      status_data: index_status_data(entry),
      estimate_text_length: estimate_text_from_entry(entry)&.length
    }
  end

  def index_status_data(entry)
    case entry["type"]
    when "model_change"
      { type: "model_change", provider: entry["provider"], model_id: entry["modelId"] || entry["model"] }
    when "thinking_level_change"
      { type: "thinking_level_change", thinking_level: entry["thinkingLevel"] || entry["thinking_level"] }
    when "message"
      message = entry["message"]
      return unless message.is_a?(Hash)
      if native_bash_execution_message?(message)
        return { type: "bash_execution", excluded_from_context: message["excludeFromContext"] == true }
      end
      return unless message["role"] == "assistant"

      {
        type: "assistant",
        provider: message["provider"],
        model_id: message["model"],
        usage: indexed_usage_data(message["usage"]),
        stop_reason: message["stopReason"]
      }
    when "compaction"
      {
        type: "compaction",
        summary_length: entry["summary"]&.length,
        first_kept_entry_id: entry["firstKeptEntryId"]
      }
    end
  end

  def indexed_usage_data(usage)
    return unless usage.is_a?(Hash)

    keys = %w[totalTokens total_tokens tokens contextWindow context_window contextLimit context_limit contextPercent context_percent costTotal cost_total]
    usage.slice(*keys).tap do |indexed|
      indexed["cost"] = { "total" => usage.dig("cost", "total") } if usage["cost"].is_a?(Hash)
    end
  end

  def projected_units(entries)
    return unless entries.all?(&:segments)

    subagent_sources = {}
    entries.each do |entry|
      entry.subagent_tool_call_ids.each { |tool_call_id| subagent_sources[tool_call_id] ||= entry.ordinal }
    end

    pending_tool_calls = {}
    units = []
    entries.each do |entry|
      entry.segments.each_with_index do |segment, segment_index|
        if segment.role == "toolResult" && pending_tool_calls[segment.tool_call_id]
          paired_unit = pending_tool_calls.delete(segment.tool_call_id)
          return unless paired_unit.tool_name == segment.tool_name

          paired_unit.dependencies << entry.ordinal
          paired_unit.minimum_window_bytes = combined_minimum_window_bytes(paired_unit.minimum_window_bytes, segment.paired_minimum_window_bytes)
          next
        end

        dependencies = []
        if segment.role == "toolResult" && segment.tool_name == "subagent" && subagent_sources[segment.tool_call_id]
          dependencies << subagent_sources[segment.tool_call_id]
        end
        unit = ProjectedUnit.new(
          key: [entry.ordinal, segment_index],
          entry_ordinal: entry.ordinal,
          dependencies: dependencies,
          tool_name: segment.tool_name,
          minimum_window_bytes: segment.minimum_window_bytes
        )
        units << unit
        if segment.tool_call_id && pair_tool_result?(segment.tool_name)
          pending_tool_calls[segment.tool_call_id] = unit
        end
      end
    end
    [units, subagent_sources]
  end

  def combined_minimum_window_bytes(left, right)
    return :deferred if left == :deferred || right == :deferred

    [left, right].compact.max
  end

  def render_window(path, index, units, cursor, after_cursor)
    File.open(path, "rb") do |file|
      validate_index_file!(file, index)
      parsed_entries = {}
      scanned_metadata = {}
      result = if after_cursor.nil?
        messages = []
        bytes = 0
        has_user_message = false
        (cursor - 1).downto(0) do |unit_index|
          unit = units.fetch(unit_index)
          enough_context = has_user_message || messages.length >= CONVERSATION_WINDOW_MIN_MESSAGES
          break if messages.length >= CONVERSATION_WINDOW_MAX_MESSAGES
          minimum_window_bytes = resolved_minimum_window_bytes(file, index, unit, scanned_metadata)
          if enough_context && minimum_window_bytes && bytes + minimum_window_bytes > CONVERSATION_WINDOW_BYTE_BUDGET
            break
          end

          message = render_projected_unit(file, index, unit, parsed_entries)
          message_bytes = indexed_window_message_bytes(message)
          break if enough_context && bytes + message_bytes > CONVERSATION_WINDOW_BYTE_BUDGET

          messages << message
          bytes += message_bytes
          has_user_message ||= message.role == "user"
        end
        messages.reverse!
        [messages, cursor - messages.length]
      else
        start_index = [[after_cursor.to_i, 0].max, cursor].min
        messages = []
        bytes = 0
        has_user_message = false
        units.slice(start_index...cursor).each do |unit|
          enough_context = has_user_message || messages.length >= CONVERSATION_WINDOW_MIN_MESSAGES
          break if messages.length >= CONVERSATION_WINDOW_MAX_MESSAGES
          minimum_window_bytes = resolved_minimum_window_bytes(file, index, unit, scanned_metadata)
          if enough_context && minimum_window_bytes && bytes + minimum_window_bytes > CONVERSATION_WINDOW_BYTE_BUDGET
            break
          end

          message = render_projected_unit(file, index, unit, parsed_entries)
          message_bytes = indexed_window_message_bytes(message)
          break if enough_context && bytes + message_bytes > CONVERSATION_WINDOW_BYTE_BUDGET

          messages << message
          bytes += message_bytes
          has_user_message ||= message.role == "user"
        end
        [messages, start_index]
      end
      validate_index_file!(file, index)
      raise Errno::EAGAIN unless index.valid_for_current_path?

      result
    end
  end

  def resolved_minimum_window_bytes(file, index, unit, scanned_metadata)
    return unit.minimum_window_bytes unless unit.minimum_window_bytes == :deferred

    source = index.entries.fetch(unit.entry_ordinal)
    source_metadata = scanned_metadata[source.ordinal] ||= index.scan_metadata(file, source)
    return unless source_metadata

    minimums = [source_metadata[:segments].fetch(unit.key[1]).minimum_window_bytes]
    unit.dependencies.each do |ordinal|
      dependency = index.entries.fetch(ordinal)
      next unless dependency.deferred_metadata

      metadata = scanned_metadata[ordinal] ||= index.scan_metadata(file, dependency)
      return unless metadata

      result_segment = metadata[:segments].find { |segment| segment.role == "toolResult" && segment.tool_name == unit.tool_name }
      minimums << result_segment&.paired_minimum_window_bytes
    end
    minimums.compact.max
  end

  def render_projected_unit(file, index, unit, parsed_entries)
    ordinals = [unit.entry_ordinal, *unit.dependencies].uniq.sort
    entries = ordinals.map do |ordinal|
      parsed_entries[ordinal] ||= read_indexed_entry(file, index.entries.fetch(ordinal))
    end
    messages = messages_from_entries(entries, source_ordinals: ordinals)
    messages.find { |message| message.instance_variable_get(:@gripi_source_key) == unit.key } or
      raise JSON::ParserError, "Indexed session projection changed"
  end

  def read_indexed_entry(file, indexed_entry)
    file.seek(indexed_entry.offset)
    JSON.parse(file.read(indexed_entry.length))
  end

  def validate_index_file!(file, index)
    stat = file.stat
    return if stat.dev == index.device && stat.ino == index.inode && stat.size == index.size && stat_mtime_ns(stat) == index.mtime_ns

    raise Errno::EAGAIN, "Session changed while its conversation window was read"
  end

  def indexed_window_message_bytes(message)
    text_bytes = [message.role, message.text, message.summary].compact.sum { |value| value.to_s.bytesize }
    image_bytes = Array(message.images).sum { |image| (image[:data] || image["data"]).to_s.bytesize }
    (text_bytes * 2) + image_bytes
  end

  def stable_tree_position_id_from_index(index, leaf_id)
    visited = {}
    entry = index.by_id[leaf_id]
    while entry && !visited[entry.id] && (entry.type == "custom_message" || entry.role == "user")
      visited[entry.id] = true
      leaf_id = entry.parent_id
      entry = index.by_id[leaf_id]
    end
    leaf_id
  end

  def status_from_index(index)
    latest = Status.new
    latest_usage_ordinal = nil
    latest_compaction = nil

    index.entries.each do |entry|
      data = entry.status_data
      next unless data

      case data[:type]
      when "model_change"
        latest.provider = data[:provider] unless data[:provider].to_s.empty?
        latest.model_id = data[:model_id] unless data[:model_id].to_s.empty?
      when "thinking_level_change"
        latest.thinking_level = data[:thinking_level] unless data[:thinking_level].to_s.empty?
      when "assistant"
        latest.provider = data[:provider] unless data[:provider].to_s.empty?
        latest.model_id = data[:model_id] unless data[:model_id].to_s.empty?
        message = {
          "role" => "assistant",
          "usage" => data[:usage],
          "stopReason" => data[:stop_reason]
        }
        latest_usage_ordinal = entry.ordinal if apply_usage(latest, message)
      when "compaction"
        latest_compaction = entry
      end
    end

    if latest_compaction && (!latest_usage_ordinal || latest_compaction.ordinal > latest_usage_ordinal)
      apply_indexed_compaction_estimate(latest, index, latest_compaction)
    end
    latest
  end

  def apply_indexed_compaction_estimate(status, index, compaction)
    data = compaction.status_data
    first_kept = index.by_id[data[:first_kept_entry_id]]
    kept_entries = first_kept ? index.entries.slice(first_kept.ordinal...compaction.ordinal) : []
    kept_messages = kept_entries.select { |entry| entry.type == "message" }
    estimated_messages = kept_messages.reject { |entry| entry.status_data&.dig(:type) == "bash_execution" && entry.status_data[:excluded_from_context] }
    if estimated_messages.any? { |entry| entry.estimate_text_length.nil? }
      status.context_tokens = nil
      status.context_limit = nil
      status.context_percent = nil
      status.context_estimated = nil
      return false
    end

    lengths = [data[:summary_length], *estimated_messages.map(&:estimate_text_length)].compact
    return true if lengths.empty?

    characters = lengths.sum + lengths.length - 1
    return true unless characters.positive?

    status.context_tokens = (characters / 4.0).ceil
    status.context_limit = nil
    status.context_percent = nil
    status.context_estimated = true
    true
  end

  def indexed_subagent_tool_call_context(path, index, sources, tool_call_ids)
    requested = Array(tool_call_ids).uniq
    return {} if requested.empty?

    missing = requested - sources.keys
    if missing.any?
      index.entries.each do |entry|
        entry.subagent_tool_call_ids.each do |tool_call_id|
          sources[tool_call_id] ||= entry.ordinal if missing.include?(tool_call_id)
        end
      end
    end
    ordinals = requested.filter_map { |tool_call_id| sources[tool_call_id] }.uniq.sort
    return {} if ordinals.empty?

    File.open(path, "rb") do |file|
      validate_index_file!(file, index)
      entries = ordinals.map { |ordinal| read_indexed_entry(file, index.entries.fetch(ordinal)) }
      normalized_subagent_tool_call_context(subagent_tool_calls_from_entries(entries)).slice(*requested)
    end
  end

  def messages_from_entries(entries, source_ordinals: nil)
    subagent_tool_calls = subagent_tool_calls_from_entries(entries)
    pending_tool_calls = {}
    entries.each_with_index.each_with_object([]) do |(entry, entry_index), rendered_messages|
      unpaired_messages_from_entry(entry).each_with_index do |message, segment_index|
        if source_ordinals
          message.instance_variable_set(:@gripi_source_key, [source_ordinals.fetch(entry_index), segment_index])
        end

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

  def unpaired_messages_from_entry(entry)
    return [compaction_message_from_entry(entry)] if entry["type"] == "compaction"
    error_message = error_message_from_entry(entry)
    return [error_message] if error_message
    return entry["display"] == true ? [custom_message_from_entry(entry)] : [] if entry["type"] == "custom_message"
    return messages_from_entry(entry) if entry["type"] == "message"

    []
  end

  def latest_leaf_id_from_entries(entries)
    entries.each_with_object({ leaf_id: nil }) do |entry, state|
      state[:leaf_id] = leaf_id_after_entry(entry) if tree_node_entry?(entry)
    end.fetch(:leaf_id)
  end

  def stable_tree_position_id(entries, leaf_id)
    entries_by_id = entries.filter_map { |entry| [entry["id"], entry] if entry["id"] }.to_h
    entry = entries_by_id[leaf_id]
    while entry && (entry["type"] == "custom_message" || entry.dig("message", "role") == "user")
      leaf_id = entry["parentId"]
      entry = entries_by_id[leaf_id]
    end
    leaf_id
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
        message = entry["message"]
        if message.is_a?(Hash) && message["role"] == "assistant"
          latest.provider = message["provider"] unless message["provider"].to_s.empty?
          latest.model_id = message["model"] unless message["model"].to_s.empty?
        end
        latest_usage_index = index if apply_usage(latest, message)
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

  def normalized_subagent_tool_call_context(tool_calls)
    tool_calls.transform_values do |details|
      { timestamp: details[:timestamp]&.utc&.iso8601(3), prompt: details[:prompt] }
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

  def leaf_id_after_entry(entry)
    entry["type"] == "leaf" ? entry["targetId"] : entry["id"]
  end

  def apply_usage(status, message)
    return false unless message.is_a?(Hash) && message["role"] == "assistant" && message["usage"].is_a?(Hash)

    return false if ["aborted", "error"].include?(message["stopReason"])

    usage = message["usage"]
    context_tokens = usage["totalTokens"] || usage["total_tokens"] || usage["tokens"]
    return false unless context_tokens.to_f.positive?

    status.context_tokens = context_tokens
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

    message = entry["message"]
    return unless message.is_a?(Hash)
    if native_bash_execution_message?(message)
      return if message["excludeFromContext"] == true

      return PiBashExecutionText.render(message)
    end

    content_text(message["content"])
  end

  def estimate_tokens(text)
    text = text.to_s
    return 0 if text.empty?

    (text.length / 4.0).ceil
  end

  def parse_session(path)
    session = self.class.fetch_session(path) { |stat| session_from_metadata(path, stat) }
    return unless session
    return if hide_session_with_missing_cwd?(session.cwd)

    session
  end

  def session_from_metadata(path, stat)
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

    File.foreach(path, chomp: true) do |line|
      next if line.empty? || canonical_tool_result_entry?(line)

      if first_user_message && (match = line.match(NATIVE_USER_MESSAGE_PREFIX))
        entry = { "type" => "message", "timestamp" => match[1], "message" => { "role" => "user" } }
      else
        entry = begin
          JSON.parse(line)
        rescue JSON::ParserError
          next
        end
      end

      case entry["type"]
      when "session"
        session_entry ||= entry
      when "session_info"
        latest_name = entry["name"].to_s.strip
        latest_name = nil if latest_name.empty?
      when "message"
        message = entry["message"] || {}
        message_count += 1 unless %w[toolResult bashExecution].include?(message["role"])
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
    Session.new(
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
    each_entry(path).to_a
  end

  def each_entry(path, skip_canonical_tool_results: false)
    return enum_for(__method__, path, skip_canonical_tool_results: skip_canonical_tool_results) unless block_given?

    File.foreach(path, chomp: true) do |line|
      next if line.strip.empty?
      next if skip_canonical_tool_results && canonical_tool_result_entry?(line)

      entry = begin
        JSON.parse(line)
      rescue JSON::ParserError
        next
      end
      yield entry
    end
  end

  def canonical_tool_result_entry?(line)
    line.match?(NATIVE_TOOL_RESULT_PREFIX)
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
      summary: "Conversation compacted",
      compaction: true
    )
  end

  def custom_message_from_entry(entry)
    Message.new(
      role: "custom",
      text: content_text(entry["content"]),
      timestamp: parse_time(entry["timestamp"]),
      entry_id: entry["id"],
      compact: false,
      images: content_images(entry["content"]),
      custom_type: entry["customType"]
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
    return [bash_execution_message_from_entry(entry, message)] if native_bash_execution_message?(message)

    if role == "assistant"
      assistant_messages_from_entry(message, parse_time(entry["timestamp"]))
    else
      general_subagent = general_subagent_details?(message)
      text = general_subagent ? general_subagent_text(message) : tool_result_text(message)
      text = skill_command_display_text(text) if role == "user"
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

  def native_bash_execution_message?(message)
    return false unless message.is_a?(Hash) && message["role"] == "bashExecution"
    return false unless message["command"].is_a?(String) && message["output"].is_a?(String)
    return false unless message["cancelled"] == true || message["cancelled"] == false
    return false unless message["truncated"] == true || message["truncated"] == false
    return false unless message["timestamp"].is_a?(Numeric)
    return false unless !message.key?("exitCode") || message["exitCode"].nil? || message["exitCode"].is_a?(Integer)
    return false unless !message.key?("fullOutputPath") || message["fullOutputPath"].is_a?(String)

    !message.key?("excludeFromContext") || message["excludeFromContext"] == true || message["excludeFromContext"] == false
  end

  def bash_execution_message_from_entry(entry, message)
    excluded = message["excludeFromContext"] == true
    command = display_home_path(message["command"])
    Message.new(
      role: "bashExecution",
      text: message["output"],
      timestamp: parse_time(entry["timestamp"]),
      entry_id: entry["id"],
      compact: true,
      summary: "$ #{command}",
      error: message["exitCode"].is_a?(Integer) && !message["exitCode"].zero?,
      tool_name: "bash",
      tool_transcript: true,
      bash_exit_code: message["exitCode"],
      bash_cancelled: message["cancelled"],
      bash_truncated: message["truncated"],
      bash_excluded_from_context: excluded,
      bash_full_output_path: message["fullOutputPath"] && display_home_path(message["fullOutputPath"]),
      bash_recorded_at: Time.at(message["timestamp"] / 1000.0)
    )
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

  def skill_command_display_text(text)
    match = text.match(/\A<skill name="([^"\n]+)" location="([^"\n]+)">\nReferences are relative to ([^\n]+)\.\n\n.*\n<\/skill>(?:\n\n(.*))?\z/m)
    location = match && match[2]
    canonical_location = location&.start_with?("/") && !location.end_with?("/") && !location.include?("//") && location.split("/").none? { |part| %w[. ..].include?(part) }
    return text unless canonical_location && File.dirname(location) == match[3]

    command = "/skill:#{match[1]}"
    match[4].to_s.empty? ? command : "#{command} #{match[4]}"
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

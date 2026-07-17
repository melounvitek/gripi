require "json"
require "base64"
require "open3"
require "securerandom"
require "thread"

class PiRpcClient
  class RequestTimeout < IOError; end

  DEFAULT_EVENT_BUFFER_LIMIT = 5_000
  TREE_BRIDGE_TIMEOUT = 5
  MAX_ACTIVE_TOOL_SNAPSHOTS = 16
  MAX_ACTIVE_TOOL_SNAPSHOT_BYTES = 64 * 1024
  MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES = 1_024
  ACTIVE_TOOL_SNAPSHOT_TOOL_LIMIT = 10
  ACTIVE_TOOL_SNAPSHOT_OUTPUT_BYTES = 1_024
  ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES = 4 * 1_024
  SNAPSHOT_TOOL_NAME = "subagent"
  GRIPI_EXTENSION_PATH = File.expand_path("../pi_extensions/gripi-tree.ts", __dir__)
  RUBY_ENV_KEYS = %w[
    GEM_HOME
    GEM_PATH
    RUBYLIB
    RUBYOPT
  ].freeze

  def self.start(session_path, command_prefix: ["pi"], popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GRIPI_EXTENSION_PATH, "--session", session_path)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def self.start_in_cwd(cwd, command_prefix: ["pi"], popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GRIPI_EXTENSION_PATH, chdir: cwd)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def self.pi_process_env
    unset_keys = ENV.keys.select { |key| key.start_with?("BUNDLE_", "BUNDLER_") } | RUBY_ENV_KEYS
    ENV.to_h.merge(unset_keys.to_h { |key| [key, nil] })
  end

  def self.command_prefix(node_path:, pi_path:)
    node_path = node_path.to_s.strip
    pi_path = pi_path.to_s.strip

    return ["pi"] if node_path.empty? && pi_path.empty?

    if node_path.empty? || pi_path.empty?
      raise ArgumentError, "GRIPI_NODE and GRIPI_PI must be set together to pin Pi to a specific Node runtime. Set both, or unset both to run pi from PATH."
    end

    [node_path, pi_path]
  end

  def initialize(stdin:, stdout:, stderr: nil, wait_thread: nil, event_buffer_limit: DEFAULT_EVENT_BUFFER_LIMIT, clock: -> { Time.now }, monotonic_clock: -> { Process.clock_gettime(Process::CLOCK_MONOTONIC) }, tree_bridge_timeout: TREE_BRIDGE_TIMEOUT)
    @stdin = stdin
    @stdout = stdout
    @stderr = stderr
    @wait_thread = wait_thread
    @request_sequence = 0
    @responses = {}
    @pending_ids = {}
    @bridge_statuses = {}
    @pending_bridge_status_keys = {}
    @events = []
    @event_sequence = 0
    @active_tool_events = {}
    @pending_extension_ui_dialogs = {}
    @extension_statuses = {}
    @extension_widgets = {}
    @extension_title = nil
    @event_buffer_limit = event_buffer_limit
    @clock = clock
    @monotonic_clock = monotonic_clock
    @tree_bridge_timeout = tree_bridge_timeout
    @mutex = Mutex.new
    @write_mutex = Mutex.new
    @condition = ConditionVariable.new
    @reader_running = false
    @busy = false
    @busy_since = nil
    @agent_running = false
    @settled_at = nil
    @compacting = false
    @compacting_since = nil
    @compaction_follow_ups = []
    @flushing_compaction_follow_ups = false
    @deferred_command_ids = {}
    @reader = nil
  end

  def get_state
    request("get_state", id: next_id("get_state"))
  end

  def get_messages
    request("get_messages", id: next_id("get_messages"))
  end

  def get_session_stats
    request("get_session_stats", id: next_id("get_session_stats"))
  end

  def session_position(append_cursor)
    result = session_entries_after(append_cursor)
    { known: result[:known], leaf_id: result[:leaf_id], error: result[:error] }
  end

  def session_entries_after(append_cursor)
    payload = append_cursor.to_s.empty? ? {} : { since: append_cursor }
    response = request("get_entries", id: next_id("get_entries"), **payload)
    data = response["data"] if response.is_a?(Hash) && response["success"] == true
    if data.is_a?(Hash) && data["entries"].is_a?(Array)
      return { known: true, leaf_id: data["leafId"], entries: data["entries"], error: nil }
    end

    error = response.is_a?(Hash) ? response["error"].to_s : "Pi RPC did not respond to get_entries"
    return { known: false, leaf_id: nil, entries: [], error: nil } if error.start_with?("Entry not found:")

    error = "Pi RPC get_entries failed" if error.empty?
    { known: false, leaf_id: nil, entries: [], error: error }
  end

  def get_available_models
    request("get_available_models", id: next_id("get_available_models"))
  end

  def set_model(provider, model_id)
    request("set_model", id: next_id("set_model"), provider: provider, modelId: model_id)
  end

  def set_thinking_level(level)
    request("set_thinking_level", id: next_id("set_thinking_level"), level: level)
  end

  def cycle_thinking_level
    request("cycle_thinking_level", id: next_id("cycle_thinking_level"))
  end

  def prompt(message, images = [])
    wait_for_compaction_follow_up_flush
    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("prompt", id: next_id("prompt"), **payload)
  end

  def steer(message)
    request("steer", id: next_id("steer"), message: message)
  end

  def follow_up(message, images = [])
    response = queue_follow_up_during_compaction(message, images)
    return response if response

    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("follow_up", id: next_id("follow_up"), **payload)
  end

  def queue_follow_up_during_compaction(message, images = [])
    payload = { message: message }
    payload[:images] = images unless images.empty?
    return unless queue_compaction_follow_up_payload(payload)

    { "type" => "response", "command" => "follow_up", "success" => true, "queued" => true, "compacting" => true }
  end

  def abort
    request("abort", id: next_id("abort"))
  end

  def new_session(parent_session = nil)
    payload = parent_session ? { parentSession: parent_session } : {}
    request("new_session", id: next_id("new_session"), **payload)
  end

  def switch_session(session_path)
    request("switch_session", id: next_id("switch_session"), sessionPath: session_path)
  end

  def get_commands
    request("get_commands", id: next_id("get_commands"))
  end

  def compact(custom_instructions = nil)
    payload = custom_instructions.to_s.empty? ? {} : { customInstructions: custom_instructions }
    request("compact", id: next_id("compact"), **payload)
  end

  def get_fork_messages
    request("get_fork_messages", id: next_id("get_fork_messages"))
  end

  def fork(entry_id)
    request("fork", id: next_id("fork"), entryId: entry_id)
  end

  def clone_session
    request("clone", id: next_id("clone"))
  end

  def tree_snapshot(filter_mode = nil)
    payload = filter_mode.to_s.empty? ? {} : { filter: filter_mode }
    extension_request("gripi_tree_snapshot", payload, timeout: @tree_bridge_timeout, timeout_error: "Session tree request timed out")
  end

  def tree_leaf
    extension_request("gripi_tree_leaf", {}, timeout: @tree_bridge_timeout, timeout_error: "Session tree request timed out")
  end

  def navigate_tree(entry_id, summary: "none", custom_instructions: nil)
    payload = { entryId: entry_id, summary: summary }
    payload[:customInstructions] = custom_instructions unless custom_instructions.to_s.empty?
    extension_request("gripi_tree_navigate", payload)
  end

  def set_tree_label(entry_id, label)
    normalized_label = label.to_s.strip
    extension_request("gripi_tree_label", { entryId: entry_id, label: normalized_label.empty? ? nil : normalized_label })
  end

  def set_session_name(name)
    request("set_session_name", id: next_id("set_session_name"), name: name)
  end

  def event_sequence
    @mutex.synchronize { @event_sequence }
  end

  def live_snapshot
    @mutex.synchronize do
      snapshot = {
        event_sequence: @event_sequence,
        active_tool_events: @active_tool_events.values
      }
      snapshot[:busy] = true if @busy
      snapshot[:busy_since] = @busy_since if @busy_since
      snapshot[:agent_running] = true if @agent_running
      snapshot[:compacting] = true if @compacting
      snapshot[:compacting_since] = @compacting_since if @compacting_since
      prune_expired_extension_ui_dialogs
      if @pending_extension_ui_dialogs.any? || @extension_statuses.any? || @extension_widgets.any? || @extension_title
        snapshot[:extension_ui] = {
          pending_dialogs: @pending_extension_ui_dialogs.values.reject { |dialog| dialog[:answering] }.map { |dialog| snapshot_extension_ui_dialog(dialog) },
          statuses: @extension_statuses.values,
          widgets: @extension_widgets.values,
          title: @extension_title
        }
      end
      snapshot
    end
  end

  def busy?
    @mutex.synchronize { @busy }
  end

  def busy_since
    @mutex.synchronize { @busy_since }
  end

  def agent_running?
    @mutex.synchronize { @agent_running }
  end

  def compacting?
    @mutex.synchronize { @compacting }
  end

  def settled_at
    @mutex.synchronize { @settled_at }
  end

  def events_after(after_seq)
    ensure_reader
    after_seq = after_seq.to_i
    @mutex.synchronize do
      prune_expired_extension_ui_dialogs
      oldest_seq = @events.first&.first
      missed = oldest_seq && after_seq < oldest_seq - 1
      events = if missed
        []
      else
        @events.select { |seq, _event| seq > after_seq }.filter_map { |_seq, event| extension_ui_event_for_delivery(event) }
      end
      { events: events, last_seq: @event_sequence, missed: !!missed }
    end
  end

  def close
    close_io(@stdin)
    close_io(@stdout)
    close_io(@stderr)
    terminate_process
    @reader&.join(0.2)
  end

  def request(type, id:, timeout: nil, **payload)
    command = payload.merge(id: id, type: type)
    deadline = monotonic_time + timeout if timeout
    @mutex.synchronize { @pending_ids[id] = true }
    ensure_reader
    begin
      write_command(command)
    rescue Errno::EPIPE, IOError
      @mutex.synchronize { @pending_ids.delete(id) }
      raise IOError, "Pi RPC process exited before accepting command"
    end

    @mutex.synchronize do
      loop do
        if deadline && deadline <= monotonic_time
          @pending_ids.delete(id)
          @responses.delete(id)
          raise RequestTimeout, "Pi RPC command timed out: #{type}"
        end
        return @responses.delete(id) if @responses.key?(id)
        unless @reader_running
          @pending_ids.delete(id)
          raise IOError, "Pi RPC process exited before responding to command"
        end

        wait = deadline ? [remaining_timeout(deadline), 0.1].min : 0.1
        @condition.wait(@mutex, wait)
      end
    end
  end

  private

  def extension_request(command, payload, timeout: nil, timeout_error: "Extension command timed out")
    request_id = SecureRandom.hex(8)
    status_key = "#{command}:#{request_id}"
    encoded_payload = Base64.urlsafe_encode64(JSON.generate(payload), padding: false)
    deadline = monotonic_time + timeout if timeout
    @mutex.synchronize { @pending_bridge_status_keys[status_key] = true }
    response = request(
      "prompt",
      id: next_id("prompt"),
      message: "/#{command} #{request_id} #{encoded_payload}",
      timeout: remaining_timeout(deadline)
    )
    return response unless response&.fetch("success", true)

    status = wait_for_status(status_key, timeout: remaining_timeout(deadline) || 5)
    return response.merge("success" => false, "error" => "Extension command did not complete") unless status

    result = JSON.parse(status)
    return response.merge("success" => false, "error" => "Extension command returned an invalid response") unless result.is_a?(Hash)
    return response.merge("success" => false, "error" => result["error"].to_s.empty? ? "Extension command failed" : result["error"]) unless result["ok"] == true

    response.merge("data" => result.reject { |key, _value| key == "ok" })
  rescue RequestTimeout
    { "success" => false, "error" => timeout_error }
  rescue JSON::ParserError
    response.merge("success" => false, "error" => "Extension command returned an invalid response")
  ensure
    @mutex.synchronize do
      @pending_bridge_status_keys.delete(status_key) if status_key
      @bridge_statuses.delete(status_key) if status_key
    end
  end

  def wait_for_status(status_key, timeout: 5)
    deadline = monotonic_time + timeout

    @mutex.synchronize do
      loop do
        remaining = deadline - monotonic_time
        raise RequestTimeout, "Pi RPC extension status timed out" unless remaining.positive?
        return @bridge_statuses.delete(status_key) if @bridge_statuses.key?(status_key)
        raise IOError, "Pi RPC process exited before reporting extension status" unless @reader_running

        @condition.wait(@mutex, [remaining, 0.1].min)
      end
    end
  end

  def close_io(io)
    io&.close unless io&.closed?
  rescue IOError
    nil
  end

  def terminate_process
    return unless @wait_thread&.respond_to?(:pid)

    Process.kill("TERM", @wait_thread.pid)
  rescue Errno::ESRCH, IOError
    nil
  end

  def monotonic_time
    @monotonic_clock.call
  end

  def remaining_timeout(deadline)
    return unless deadline

    [deadline - monotonic_time, 0].max
  end

  def next_id(type)
    @mutex.synchronize do
      @request_sequence += 1
      "#{type}-#{@request_sequence}"
    end
  end

  def ensure_reader
    @mutex.synchronize do
      return if @reader

      @reader_running = true
      @reader = Thread.new { read_stdout }
    end
  end

  def read_stdout
    while (line = @stdout.gets)
      next if line.strip.empty?

      begin
        response = JSON.parse(line)
        store_response(response, serialized_bytesize: line.bytesize)
      rescue JSON::ParserError
        next
      end
    end
  rescue IOError
    nil
  ensure
    @mutex.synchronize do
      @reader_running = false
      @agent_running = false
      @compacting = false
      @compacting_since = nil
      @compaction_follow_ups.clear
      @flushing_compaction_follow_ups = false
      @deferred_command_ids.clear
      @active_tool_events.clear
      @pending_extension_ui_dialogs.clear
      @extension_statuses.clear
      @extension_widgets.clear
      @extension_title = nil
      @busy = false
      @busy_since = nil
      @condition.broadcast
    end
  end

  public

  def extension_ui_response(id, value: nil, confirmed: nil, cancelled: false)
    id = id.to_s
    command = { type: "extension_ui_response", id: id }
    if cancelled
      command[:cancelled] = true
    elsif !confirmed.nil?
      command[:confirmed] = !!confirmed
    else
      command[:value] = value.to_s
    end

    @write_mutex.synchronize do
      dialog = @mutex.synchronize do
        prune_expired_extension_ui_dialogs
        @pending_extension_ui_dialogs[id]&.tap { |pending| pending[:answering] = true }
      end
      return { "type" => "response", "command" => "extension_ui_response", "success" => false, "error" => "Extension UI request is no longer pending" } unless dialog

      begin
        write_command_unlocked(command)
        @mutex.synchronize { @pending_extension_ui_dialogs.delete(id) }
      rescue Errno::EPIPE, IOError
        @mutex.synchronize do
          if @pending_extension_ui_dialogs[id].equal?(dialog)
            dialog[:answering] = false
            @event_sequence += 1
            @events << [@event_sequence, dialog[:event]]
            @events.shift while @events.length > @event_buffer_limit
            @condition.broadcast
          end
        end
        raise
      end
    end
    { "type" => "response", "command" => "extension_ui_response", "success" => true }
  end

  private

  def write_command(command)
    @write_mutex.synchronize { write_command_unlocked(command) }
  end

  def write_command_unlocked(command)
    @stdin.write(JSON.generate(command) + "\n")
    @stdin.flush if @stdin.respond_to?(:flush)
  end

  def queue_compaction_follow_up_payload(payload)
    @mutex.synchronize do
      return false unless @compacting || @flushing_compaction_follow_ups

      @compaction_follow_ups << payload
      true
    end
  end

  def wait_for_compaction_follow_up_flush
    @mutex.synchronize do
      @condition.wait(@mutex, 0.1) while @flushing_compaction_follow_ups || (@compacting && @compaction_follow_ups.any?)
    end
  end

  def begin_compaction_follow_up_flush
    follow_ups = @compaction_follow_ups
    @compaction_follow_ups = []
    @flushing_compaction_follow_ups = true if follow_ups.any?
    follow_ups
  end

  def flush_compaction_follow_ups(follow_ups, first_type = "prompt")
    @write_mutex.synchronize do
      write_first_compaction_follow_up_batch(follow_ups, first_type)
      loop do
        follow_ups = @mutex.synchronize do
          if @compaction_follow_ups.empty?
            @flushing_compaction_follow_ups = false
            @condition.broadcast
            return
          end

          queued = @compaction_follow_ups
          @compaction_follow_ups = []
          queued
        end
        follow_ups.each { |payload| write_queued_prompt_unlocked("follow_up", payload) }
      end
    end
  rescue Errno::EPIPE, IOError
    @mutex.synchronize do
      @compaction_follow_ups.clear
      @flushing_compaction_follow_ups = false
      @condition.broadcast
    end
  end

  def write_first_compaction_follow_up_batch(follow_ups, first_type)
    first, *rest = follow_ups
    write_queued_prompt_unlocked(first_type, first) if first
    rest.each { |payload| write_queued_prompt_unlocked("follow_up", payload) }
  end

  def write_queued_prompt_unlocked(type, payload)
    id = next_id(type)
    @mutex.synchronize { @deferred_command_ids[id] = true }
    write_command_unlocked(payload.merge(id: id, type: type))
  rescue Errno::EPIPE, IOError
    @mutex.synchronize { @deferred_command_ids.delete(id) }
    raise
  end

  def store_response(response, serialized_bytesize:)
    follow_ups_to_flush = nil
    first_follow_up_type = "prompt"
    @mutex.synchronize do
      store_as_event = false
      status_key = internal_bridge_status_key(response)
      if status_key
        @bridge_statuses[status_key] = response["statusText"] if @pending_bridge_status_keys.key?(status_key)
      elsif response["id"] && @pending_ids.delete(response["id"])
        @responses[response["id"]] = response
      elsif response["id"] && @deferred_command_ids.delete(response["id"])
        store_as_event = response["success"] == false
      elsif response["type"] == "response" && response["id"]
        # A timed-out RPC response is no longer useful and must not become an event.
      else
        store_as_event = true
      end

      if store_as_event
        if ["agent_start", "agent_settled", "turn_start", "compaction_start"].include?(response["type"])
          response = response.merge("gatewayTimestamp" => (@clock.call.to_f * 1000).to_i)
        end
        update_busy_state(response)
        update_extension_ui_state(response)
        if ["compaction", "compaction_end"].include?(response["type"])
          follow_ups_to_flush = begin_compaction_follow_up_flush
          first_follow_up_type = "follow_up" if response["type"] == "compaction_end" && response["willRetry"] == true
        end
        update_active_tool_events(response, serialized_bytesize)
        @event_sequence += 1
        @events << [@event_sequence, response]
        @events.shift while @events.length > @event_buffer_limit
      end
      @condition.broadcast
    end
    flush_compaction_follow_ups(follow_ups_to_flush, first_follow_up_type) if follow_ups_to_flush&.any?
  end

  def update_extension_ui_state(response)
    return unless response["type"] == "extension_ui_request"

    prune_expired_extension_ui_dialogs
    case response["method"]
    when "select", "confirm", "input", "editor"
      id = response["id"].to_s
      return if id.empty?

      timeout = response["timeout"]
      expires_at = monotonic_time + timeout.to_f / 1000 if timeout.is_a?(Numeric) && timeout.positive?
      @pending_extension_ui_dialogs[id] = { event: response, expires_at: expires_at }
    when "setStatus"
      key = response["statusKey"].to_s
      return if key.empty?

      if response["statusText"].nil? || response["statusText"] == ""
        @extension_statuses.delete(key)
      else
        @extension_statuses[key] = response
      end
    when "setWidget"
      key = response["widgetKey"].to_s
      return if key.empty?

      if response["widgetLines"].is_a?(Array)
        @extension_widgets[key] = response
      else
        @extension_widgets.delete(key)
      end
    when "setTitle"
      @extension_title = response["title"].nil? ? nil : response
    end
  end

  def prune_expired_extension_ui_dialogs
    now = monotonic_time
    @pending_extension_ui_dialogs.delete_if { |_id, dialog| dialog[:expires_at] && dialog[:expires_at] <= now }
  end

  def extension_ui_event_for_delivery(event)
    return event unless event["type"] == "extension_ui_request" && ["select", "confirm", "input", "editor"].include?(event["method"])

    dialog = @pending_extension_ui_dialogs[event["id"].to_s]
    snapshot_extension_ui_dialog(dialog) if dialog && !dialog[:answering]
  end

  def snapshot_extension_ui_dialog(dialog)
    event = dialog[:event]
    return event unless dialog[:expires_at]

    remaining_timeout = [((dialog[:expires_at] - monotonic_time) * 1000).ceil, 0].max
    event.merge("timeout" => remaining_timeout)
  end

  def internal_bridge_status_key(response)
    return unless response["type"] == "extension_ui_request" && response["method"] == "setStatus"

    status_key = response["statusKey"]
    status_key if status_key.to_s.match?(/\Agripi_tree_(?:snapshot|leaf|navigate|label):[a-f0-9]+\z/i)
  end

  def update_active_tool_events(response, serialized_bytesize)
    if response["type"] == "agent_end"
      @active_tool_events.clear
      return
    end

    tool_call_id = response["toolCallId"]
    return unless tool_call_id.is_a?(String) && tool_call_id.bytesize <= MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES

    if response["type"] == "tool_execution_end"
      @active_tool_events.delete(tool_call_id)
    elsif ["tool_execution_start", "tool_execution_update"].include?(response["type"]) && response["toolName"] == SNAPSHOT_TOOL_NAME
      return if !@active_tool_events.key?(tool_call_id) && @active_tool_events.length >= MAX_ACTIVE_TOOL_SNAPSHOTS

      snapshot = bounded_active_tool_event(response, serialized_bytesize)
      @active_tool_events[tool_call_id] = snapshot if snapshot
    end
  end

  def bounded_active_tool_event(response, serialized_bytesize)
    return response if serialized_bytesize <= MAX_ACTIVE_TOOL_SNAPSHOT_BYTES

    result = response["partialResult"]
    details = result["details"] if result.is_a?(Hash)
    snapshot = general_subagent_snapshot(response, result, details) if details.is_a?(Hash) && details["tools"].is_a?(Array) && details["usage"].is_a?(Hash)
    return snapshot if snapshot && JSON.generate(snapshot).bytesize <= MAX_ACTIVE_TOOL_SNAPSHOT_BYTES

    fallback = subagent_fallback_snapshot(response, result)
    fallback if JSON.generate(fallback).bytesize <= MAX_ACTIVE_TOOL_SNAPSHOT_BYTES
  end

  def general_subagent_snapshot(response, result, details)
    text_items = details["textItems"].is_a?(Array) ? details["textItems"].last(1) : []
    compact_details = {
      "task" => bounded_snapshot_text(details["task"], ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES),
      "model" => bounded_snapshot_text(details["model"], 512),
      "status" => bounded_snapshot_text(details["status"], 256),
      "tools" => details["tools"].last(ACTIVE_TOOL_SNAPSHOT_TOOL_LIMIT).filter_map { |tool| compact_snapshot_tool(tool) },
      "textItems" => text_items.map { |text| bounded_snapshot_text(text, ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES) },
      "streamingText" => bounded_snapshot_text(details["streamingText"], ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES),
      "usage" => compact_snapshot_values(details["usage"])
    }.compact

    {
      "type" => "tool_execution_update",
      "toolCallId" => response["toolCallId"],
      "toolName" => SNAPSHOT_TOOL_NAME,
      "partialResult" => {
        "content" => compact_snapshot_content(result["content"]),
        "details" => compact_details
      }
    }
  end

  def compact_snapshot_tool(tool)
    return unless tool.is_a?(Hash)

    {
      "name" => bounded_snapshot_text(tool["name"], 256),
      "args" => compact_snapshot_values(tool["args"]),
      "status" => bounded_snapshot_text(tool["status"], 256),
      "output" => bounded_snapshot_text(tool["output"], ACTIVE_TOOL_SNAPSHOT_OUTPUT_BYTES)
    }.compact
  end

  def compact_snapshot_content(content)
    Array(content).last(1).filter_map do |part|
      next unless part.is_a?(Hash) && part["type"] == "text"

      { "type" => "text", "text" => bounded_snapshot_text(part["text"], ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES) }
    end
  end

  def compact_snapshot_values(values)
    return {} unless values.is_a?(Hash)

    values.first(12).to_h do |key, value|
      compact_value = value.is_a?(String) ? bounded_snapshot_text(value, 512) : value
      compact_value = nil if compact_value.is_a?(Float) && !compact_value.finite?
      compact_value = nil unless compact_value.nil? || compact_value.is_a?(Numeric) || compact_value == true || compact_value == false || compact_value.is_a?(String)
      [key, compact_value]
    end.compact
  end

  def bounded_snapshot_text(value, byte_limit)
    text = value.to_s
    return text if text.bytesize <= byte_limit

    omission = "\n…\n"
    available = byte_limit - omission.bytesize
    head_limit = available / 2
    tail_limit = available - head_limit
    head = text.byteslice(0, head_limit)
    head = head.byteslice(0, head.bytesize - 1) until head.valid_encoding?
    tail_start = text.bytesize - tail_limit
    tail_start += 1 until (tail = text.byteslice(tail_start, text.bytesize - tail_start)).valid_encoding?
    "#{head}#{omission}#{tail}"
  end

  def subagent_fallback_snapshot(response, result)
    text = compact_snapshot_content(result.is_a?(Hash) ? result["content"] : nil).dig(0, "text")
    text = "Subagent is still running…" if text.to_s.empty?
    {
      "type" => "tool_execution_update",
      "toolCallId" => response["toolCallId"],
      "toolName" => SNAPSHOT_TOOL_NAME,
      "partialResult" => { "content" => [{ "type" => "text", "text" => text }] }
    }
  end

  def update_busy_state(response)
    case response["type"]
    when "agent_start"
      @agent_running = true
      @settled_at = nil
      @busy = true
      @busy_since ||= gateway_event_time(response)
    when "compaction_start"
      mark_compacting(gateway_event_time(response))
    when "turn_start"
      @busy = true
      @busy_since ||= gateway_event_time(response)
    when "turn_end"
      clear_busy_state unless @agent_running
    when "agent_settled"
      @agent_running = false
      @settled_at = gateway_event_time(response)
      clear_busy_state
    when "compaction", "compaction_end"
      clear_compacting
    end
  end

  def gateway_event_time(response)
    Time.at(response.fetch("gatewayTimestamp") / 1000.0)
  end

  def mark_compacting(started_at)
    @compacting = true
    @compacting_since ||= started_at
    @busy = true
    @busy_since ||= @compacting_since
  end

  def clear_compacting
    @compacting = false
    @compacting_since = nil
    clear_busy_state unless @agent_running
  end

  def clear_busy_state
    @busy = false
    @busy_since = nil
  end
end

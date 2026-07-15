require "json"
require "open3"
require "securerandom"
require "thread"

class PiRpcClient
  DEFAULT_EVENT_BUFFER_LIMIT = 5_000
  MAX_ACTIVE_TOOL_SNAPSHOTS = 16
  MAX_ACTIVE_TOOL_SNAPSHOT_BYTES = 64 * 1024
  MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES = 1_024
  ACTIVE_TOOL_SNAPSHOT_TOOL_LIMIT = 10
  ACTIVE_TOOL_SNAPSHOT_OUTPUT_BYTES = 1_024
  ACTIVE_TOOL_SNAPSHOT_TEXT_BYTES = 4 * 1_024
  SNAPSHOT_TOOL_NAME = "subagent"
  CANCELLED_EXTENSION_DIALOG_METHODS = %w[select confirm input editor].freeze
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

  def initialize(stdin:, stdout:, stderr: nil, wait_thread: nil, event_buffer_limit: DEFAULT_EVENT_BUFFER_LIMIT, clock: -> { Time.now })
    @stdin = stdin
    @stdout = stdout
    @stderr = stderr
    @wait_thread = wait_thread
    @request_sequence = 0
    @responses = {}
    @pending_ids = {}
    @events = []
    @event_sequence = 0
    @active_tool_events = {}
    @event_buffer_limit = event_buffer_limit
    @clock = clock
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
    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("prompt", id: next_id("prompt"), **payload)
  end

  def steer(message)
    request("steer", id: next_id("steer"), message: message)
  end

  def follow_up(message, images = [])
    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("follow_up", id: next_id("follow_up"), **payload)
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

  def navigate_tree(entry_id)
    request_id = SecureRandom.hex(8)
    response = request("prompt", id: next_id("prompt"), message: "/gripi_tree #{entry_id} #{request_id}")
    return response unless response&.fetch("success", true)

    result = wait_for_status("gripi_tree:#{request_id}")
    return response.merge("success" => false, "error" => "Tree navigation did not complete") unless result

    response.merge("data" => { "cancelled" => result == "cancelled" })
  end

  def tree_leaf
    request_id = SecureRandom.hex(8)
    response = request("prompt", id: next_id("prompt"), message: "/gripi_tree_leaf #{request_id}")
    return unless response&.fetch("success", true)

    result = wait_for_status("gripi_tree_leaf:#{request_id}")
    result == "__root__" ? nil : result
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
      oldest_seq = @events.first&.first
      missed = oldest_seq && after_seq < oldest_seq - 1
      events = missed ? [] : @events.select { |seq, _event| seq > after_seq }.map(&:last)
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

  def request(type, id:, **payload)
    command = payload.merge(id: id, type: type)
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
        return @responses.delete(id) if @responses.key?(id)
        unless @reader_running
          @pending_ids.delete(id)
          raise IOError, "Pi RPC process exited before responding to command"
        end

        @condition.wait(@mutex, 0.1)
      end
    end
  end

  private

  def wait_for_status(status_key, timeout: 5)
    deadline = @clock.call + timeout

    @mutex.synchronize do
      loop do
        result = status_from_events(status_key)
        return result if result
        raise IOError, "Pi RPC process exited before reporting extension status" unless @reader_running

        remaining = deadline - @clock.call
        return nil unless remaining.positive?

        @condition.wait(@mutex, [remaining, 0.1].min)
      end
    end
  end

  def status_from_events(status_key)
    event = @events.reverse_each.find do |_seq, candidate|
      candidate["type"] == "extension_ui_request" &&
        candidate["method"] == "setStatus" &&
        candidate["statusKey"] == status_key
    end
    event&.last&.[]("statusText")
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
        cancel_unsupported_extension_dialog(response)
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
      @active_tool_events.clear
      @busy = false
      @busy_since = nil
      @condition.broadcast
    end
  end

  def cancel_unsupported_extension_dialog(response)
    return unless response["type"] == "extension_ui_request"
    return unless CANCELLED_EXTENSION_DIALOG_METHODS.include?(response["method"])
    return if response["id"].to_s.empty?

    write_command(type: "extension_ui_response", id: response["id"], cancelled: true)
  end

  def write_command(command)
    @write_mutex.synchronize do
      @stdin.write(JSON.generate(command) + "\n")
      @stdin.flush if @stdin.respond_to?(:flush)
    end
  end

  def store_response(response, serialized_bytesize:)
    @mutex.synchronize do
      if response["id"] && @pending_ids.delete(response["id"])
        @responses[response["id"]] = response
      else
        if ["agent_start", "agent_settled", "turn_start", "compaction_start"].include?(response["type"])
          response = response.merge("gatewayTimestamp" => (@clock.call.to_f * 1000).to_i)
        end
        update_busy_state(response)
        update_active_tool_events(response, serialized_bytesize)
        @event_sequence += 1
        @events << [@event_sequence, response]
        @events.shift while @events.length > @event_buffer_limit
      end
      @condition.broadcast
    end
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

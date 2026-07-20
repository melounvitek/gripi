require "json"
require "base64"
require "open3"
require "securerandom"
require "thread"
require_relative "rpc/diagnostics"

class PiRpcClient
  class RequestTimeout < IOError; end
  class BashAlreadyRunning < StandardError; end
  class BashRequestFailed < IOError
    attr_reader :bash_id

    def initialize(bash_id, message)
      @bash_id = bash_id
      super(message)
    end
  end

  DEFAULT_EVENT_BUFFER_LIMIT = 5_000
  DEFAULT_EVENT_BUFFER_BYTES = 8 * 1024 * 1024
  RPC_READ_CHUNK_BYTES = 64 * 1024
  MAX_SAMPLED_TOOL_UPDATE_BYTES = 1024 * 1024
  DEFAULT_OVERSIZED_TOOL_UPDATE_SAMPLE_INTERVAL_SECONDS = 2
  MAX_OVERSIZED_TOOL_UPDATE_SAMPLE_KEYS = 64
  # Native Pi emits these keys first. Other JSON layouts retain the existing unbounded compatibility path.
  NATIVE_TOOL_UPDATE_PREFIX = /\A\{"type":"tool_execution_update","toolCallId":"((?:\\.|[^"\\])*)"/.freeze
  NATIVE_TOOL_UPDATE_START = '{"type":"tool_execution_update","toolCallId":"'.freeze
  UNTRACKABLE_TOOL_UPDATE_ID = Object.new.freeze
  DEFAULT_REQUEST_TIMEOUT = 30
  LONG_REQUEST_TIMEOUT = 5 * 60
  ABORT_REQUEST_TIMEOUT = 10
  PROCESS_TERM_TIMEOUT = 1
  PROCESS_KILL_TIMEOUT = 1
  TREE_BRIDGE_TIMEOUT = 5
  MAX_ACTIVE_TOOL_SNAPSHOTS = 16
  MAX_COMPLETED_BASH_EVENTS = 16
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
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GRIPI_EXTENSION_PATH, "--session", session_path, **process_group_options)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread, process_group: true)
  end

  def self.start_in_cwd(cwd, command_prefix: ["pi"], popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GRIPI_EXTENSION_PATH, **process_group_options.merge(chdir: cwd))
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread, process_group: true)
  end

  def self.process_group_options
    Gem.win_platform? ? { new_pgroup: true } : { pgroup: true }
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

  def initialize(stdin:, stdout:, stderr: nil, wait_thread: nil, event_buffer_limit: DEFAULT_EVENT_BUFFER_LIMIT, event_buffer_bytes: DEFAULT_EVENT_BUFFER_BYTES, clock: -> { Time.now }, monotonic_clock: -> { Process.clock_gettime(Process::CLOCK_MONOTONIC) }, tree_bridge_timeout: TREE_BRIDGE_TIMEOUT, oversized_tool_update_sample_interval_seconds: DEFAULT_OVERSIZED_TOOL_UPDATE_SAMPLE_INTERVAL_SECONDS, request_timeout: DEFAULT_REQUEST_TIMEOUT, abort_timeout: ABORT_REQUEST_TIMEOUT, process_group: false, process_term_timeout: PROCESS_TERM_TIMEOUT, process_kill_timeout: PROCESS_KILL_TIMEOUT)
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
    @event_buffer_bytesize = 0
    @event_replay_floor = 0
    @coalesced_events = {}
    @event_sequence = 0
    @active_tool_events = {}
    @oversized_tool_update_sampled_at = {}
    @queued_messages = { "steering" => [], "followUp" => [] }
    @pending_extension_ui_dialogs = {}
    @extension_statuses = {}
    @extension_widgets = {}
    @extension_title = nil
    @event_buffer_limit = event_buffer_limit
    @event_buffer_bytes = event_buffer_bytes
    @clock = clock
    @monotonic_clock = monotonic_clock
    @tree_bridge_timeout = tree_bridge_timeout
    @oversized_tool_update_sample_interval_seconds = oversized_tool_update_sample_interval_seconds
    @request_timeout = request_timeout
    @abort_timeout = abort_timeout
    @process_group = process_group
    @process_term_timeout = process_term_timeout
    @process_kill_timeout = process_kill_timeout
    @mutex = Mutex.new
    @write_mutex = Mutex.new
    @close_mutex = Mutex.new
    @process_cleanup_mutex = Mutex.new
    @condition = ConditionVariable.new
    @reader_running = false
    @busy = false
    @busy_since = nil
    @agent_running = false
    @settled_at = nil
    @active_bash_token = nil
    @active_bash_id = nil
    @active_bash_command = nil
    @active_bash_exclude_from_context = false
    @active_bash_started_at = nil
    @completed_bash_events = {}
    @compacting = false
    @compacting_since = nil
    @compaction_follow_ups = []
    @flushing_compaction_follow_ups = false
    @deferred_command_ids = {}
    @reader = nil
    @stderr_reader = Thread.new { read_stderr } if @stderr
    @closed = false
    if @process_group && !Gem.win_platform? && @wait_thread&.respond_to?(:join)
      @process_watcher = Thread.new do
        @wait_thread.join
        terminate_remaining_process_group
      end
    end
  end

  def get_state
    request("get_state", id: next_id("get_state"))
  end

  def get_state_for_interrupt
    request("get_state", id: next_id("get_state"), timeout: @abort_timeout)
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
    deadline = monotonic_time + @request_timeout if @request_timeout
    wait_for_compaction_follow_up_flush(deadline)
    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("prompt", id: next_id("prompt"), timeout: remaining_timeout(deadline), **payload)
  end

  def steer(message, images = [])
    payload = { message: message }
    payload[:images] = images unless images.empty?
    request("steer", id: next_id("steer"), **payload)
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
    request("abort", id: next_id("abort"), timeout: @abort_timeout)
  end

  def bash(command, exclude_from_context: false)
    bash_id = "bash-#{SecureRandom.hex(16)}"
    token = begin_bash(bash_id, command, exclude_from_context, @clock.call)
    payload = { command: command }
    payload[:excludeFromContext] = true if exclude_from_context
    response = request("bash", id: bash_id, timeout: nil, **payload) { start_bash(token) }
    if response.is_a?(Hash) && response["success"] == true
      complete_bash(token, "bash_end", "result", response["data"], command: command, exclude_from_context: exclude_from_context)
    else
      error = response.is_a?(Hash) ? response["error"] : nil
      complete_bash(token, "bash_error", "error", error.to_s.empty? ? "Bash command failed" : error, command: command, exclude_from_context: exclude_from_context)
    end
    response
  rescue StandardError => error
    accepted = token && @mutex.synchronize { token[:started] }
    complete_bash(token, "bash_error", "error", error.message, command: command, exclude_from_context: exclude_from_context) if accepted
    raise BashRequestFailed.new(token.fetch(:id), error.message), cause: error if accepted

    raise
  ensure
    @mutex.synchronize { clear_active_bash_state if @active_bash_token.equal?(token) } if token
  end

  def abort_bash
    request("abort_bash", id: next_id("abort_bash"), timeout: @abort_timeout)
  end

  def new_session(parent_session = nil)
    payload = parent_session ? { parentSession: parent_session } : {}
    request("new_session", id: next_id("new_session"), timeout: LONG_REQUEST_TIMEOUT, **payload)
  end

  def switch_session(session_path)
    request("switch_session", id: next_id("switch_session"), timeout: LONG_REQUEST_TIMEOUT, sessionPath: session_path)
  end

  def get_commands
    request("get_commands", id: next_id("get_commands"))
  end

  def compact(custom_instructions = nil)
    payload = custom_instructions.to_s.empty? ? {} : { customInstructions: custom_instructions }
    request("compact", id: next_id("compact"), timeout: LONG_REQUEST_TIMEOUT, **payload)
  end

  def get_fork_messages
    request("get_fork_messages", id: next_id("get_fork_messages"))
  end

  def fork(entry_id)
    request("fork", id: next_id("fork"), timeout: LONG_REQUEST_TIMEOUT, entryId: entry_id)
  end

  def clone_session
    request("clone", id: next_id("clone"), timeout: LONG_REQUEST_TIMEOUT)
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
    extension_request("gripi_tree_navigate", payload, timeout: LONG_REQUEST_TIMEOUT)
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

  def event_replay_cursor
    @mutex.synchronize { @event_replay_floor }
  end

  def live_snapshot
    @mutex.synchronize do
      snapshot = {
        event_sequence: @event_sequence,
        event_replay_cursor: @event_replay_floor,
        active_tool_events: @active_tool_events.values
      }
      snapshot[:busy] = true if busy_locked?
      snapshot[:busy_since] = busy_since_locked if busy_since_locked
      snapshot[:agent_busy_since] = @busy_since if @busy_since
      snapshot[:agent_running] = true if @agent_running
      if @active_bash_started_at
        snapshot[:active_bash] = {
          bash_id: @active_bash_id,
          command: @active_bash_command,
          exclude_from_context: @active_bash_exclude_from_context,
          started_at: @active_bash_started_at
        }
      end
      snapshot[:completed_bash_events] = @completed_bash_events.values if @completed_bash_events.any?
      snapshot[:compacting] = true if @compacting
      snapshot[:compacting_since] = @compacting_since if @compacting_since
      if @queued_messages.values.any?(&:any?)
        snapshot[:queued_messages] = @queued_messages.transform_values(&:dup)
      end
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
    @mutex.synchronize { busy_locked? }
  end

  def busy_since
    @mutex.synchronize { busy_since_locked }
  end

  def active_bash_command
    @mutex.synchronize { @active_bash_command }
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
      missed = after_seq < @event_replay_floor
      events = if missed
        []
      else
        @events.select { |seq, _event, _bytes| seq > after_seq }.filter_map { |_seq, event, _bytes| extension_ui_event_for_delivery(event) }
      end
      { events: events, last_seq: @event_sequence, missed: !!missed }
    end
  end

  def close
    @close_mutex.synchronize do
      return if @closed

      @closed = true
      close_io(@stdin)
      close_io(@stdout)
      close_io(@stderr)
      terminate_process
      @reader&.join(0.2)
      @stderr_reader&.join(0.2)
      @process_watcher&.join(@process_kill_timeout)
    end
  end

  def request(type, id:, timeout: @request_timeout, **payload)
    command = payload.merge(id: id, type: type)
    Rpc::Diagnostics.log("command_started", command: type, rpc_id: id)
    deadline = monotonic_time + timeout if timeout
    @mutex.synchronize { @pending_ids[id] = true }
    ensure_reader
    begin
      write_command(command, deadline: deadline, type: type) { yield if block_given? }
    rescue RequestTimeout
      @mutex.synchronize { @pending_ids.delete(id) }
      Rpc::Diagnostics.log("command_timed_out", command: type, rpc_id: id, stage: "write")
      raise
    rescue Errno::EPIPE, IOError
      @mutex.synchronize { @pending_ids.delete(id) }
      raise IOError, "Pi RPC process exited before accepting command"
    end

    @mutex.synchronize do
      loop do
        if deadline && deadline <= monotonic_time
          @pending_ids.delete(id)
          @responses.delete(id)
          Rpc::Diagnostics.log("command_timed_out", command: type, rpc_id: id, stage: "response")
          raise RequestTimeout, "Pi RPC command timed out: #{type}"
        end
        if @responses.key?(id)
          Rpc::Diagnostics.log("response_received", command: type, rpc_id: id)
          return @responses.delete(id)
        end
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
    timeout ||= @request_timeout
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

  def read_stderr
    @stderr.readpartial(4 * 1024) while true
  rescue EOFError, IOError
    nil
  end

  def close_io(io)
    io&.close unless io&.closed?
  rescue IOError
    nil
  end

  def terminate_process
    return unless @wait_thread&.respond_to?(:pid)
    return terminate_windows_process_tree if @process_group && Gem.win_platform?
    if @wait_thread.respond_to?(:alive?) && !@wait_thread.alive?
      @process_watcher&.join(@process_kill_timeout)
      return
    end

    Rpc::Diagnostics.log("process_terminating", pid: @wait_thread.pid, signal: "TERM", process_group: @process_group)
    return unless signal_process("TERM")

    parent_exited = @wait_thread.respond_to?(:join) && @wait_thread.join(@process_term_timeout)
    if @process_group
      parent_exited ? @process_watcher&.join(@process_kill_timeout) : terminate_remaining_process_group
    elsif !parent_exited
      Rpc::Diagnostics.log("process_terminating", pid: @wait_thread.pid, signal: "KILL", process_group: false)
      signal_process("KILL")
    end
    @wait_thread.join(@process_kill_timeout) if @wait_thread.respond_to?(:join)
  rescue Errno::ESRCH, IOError
    nil
  end

  def terminate_remaining_process_group
    @process_cleanup_mutex.synchronize do
      return unless process_group_alive?

      Rpc::Diagnostics.log("process_terminating", pid: @wait_thread.pid, signal: "KILL", process_group: true)
      signal_process("KILL")
    end
  end

  def terminate_windows_process_tree
    return if @wait_thread.respond_to?(:alive?) && !@wait_thread.alive?

    Rpc::Diagnostics.log("process_terminating", pid: @wait_thread.pid, signal: "KILL", process_group: true)
    killer = Process.spawn("taskkill", "/PID", @wait_thread.pid.to_s, "/T", "/F", out: File::NULL, err: File::NULL)
    killer_wait = Process.detach(killer)
    finished = killer_wait.join(@process_kill_timeout)
    unless finished
      Process.kill("KILL", killer)
      finished = killer_wait.join(@process_kill_timeout)
    end
    signal_process("KILL") unless finished && finished.value.success?
    @wait_thread.join(@process_kill_timeout) if @wait_thread.respond_to?(:join)
  rescue Errno::ENOENT, Errno::ESRCH
    signal_process("KILL")
  end

  def process_group_alive?
    return false unless @process_group && !Gem.win_platform?

    Process.kill(0, -@wait_thread.pid)
    true
  rescue Errno::ESRCH
    false
  end

  def signal_process(signal)
    pid = @wait_thread.pid
    target = @process_group && !Gem.win_platform? ? -pid : pid
    Process.kill(signal, target)
    true
  rescue Errno::ESRCH
    false
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

  def each_stdout_line
    chunk = +"".b
    line = +"".b
    mode = :unclassified

    loop do
      @stdout.readpartial(RPC_READ_CHUNK_BYTES, chunk)
      chunk.force_encoding(Encoding::BINARY)
      offset = 0
      while offset < chunk.bytesize
        newline = chunk.index("\n", offset)
        length = (newline ? newline + 1 : chunk.bytesize) - offset

        unless mode == :discard
          if offset.zero? && length == chunk.bytesize
            line << chunk
          else
            line << chunk.byteslice(offset, length)
          end

          if mode == :unclassified && line.bytesize >= RPC_READ_CHUNK_BYTES
            tool_call_id = native_oversized_tool_update_id(line)
            mode = if tool_call_id.equal?(UNTRACKABLE_TOOL_UPDATE_ID)
              :discard
            elsif tool_call_id
              sample_oversized_tool_update?(tool_call_id) ? :sampled : :discard
            else
              :fallback
            end
            line.clear if mode == :discard
          elsif mode == :sampled && line.bytesize > MAX_SAMPLED_TOOL_UPDATE_BYTES
            mode = :discard
            line.clear
          end
        end

        if newline
          yield line unless mode == :discard
          line.clear
          mode = :unclassified
        end
        offset += length
      end
    end
  rescue EOFError
    yield line unless line.empty? || mode == :discard
  end

  def native_oversized_tool_update_id(fragment)
    return unless fragment.start_with?(NATIVE_TOOL_UPDATE_START)

    encoded_id = fragment.match(NATIVE_TOOL_UPDATE_PREFIX)&.[](1)
    return UNTRACKABLE_TOOL_UPDATE_ID unless encoded_id

    tool_call_id = JSON.parse(%("#{encoded_id}"))
    return UNTRACKABLE_TOOL_UPDATE_ID if tool_call_id.bytesize > MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES

    tool_call_id
  rescue JSON::ParserError
    UNTRACKABLE_TOOL_UPDATE_ID
  end

  def sample_oversized_tool_update?(tool_call_id)
    now = monotonic_time
    @oversized_tool_update_sampled_at.delete_if do |_id, sampled_at|
      now - sampled_at >= @oversized_tool_update_sample_interval_seconds
    end
    return false if @oversized_tool_update_sampled_at.key?(tool_call_id)
    return false if @oversized_tool_update_sampled_at.length >= MAX_OVERSIZED_TOOL_UPDATE_SAMPLE_KEYS

    @oversized_tool_update_sampled_at[tool_call_id] = now
    true
  end

  def read_stdout
    each_stdout_line do |line|
      next if line.match?(/\A[[:space:]]*\z/)

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
      clear_active_bash_state
      @compacting = false
      @compacting_since = nil
      @compaction_follow_ups.clear
      @flushing_compaction_follow_ups = false
      @deferred_command_ids.clear
      @active_tool_events.clear
      @completed_bash_events.clear
      @queued_messages = { "steering" => [], "followUp" => [] }
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

    deadline = monotonic_time + @request_timeout if @request_timeout
    with_write_lock(deadline, "extension_ui_response") do
      dialog = @mutex.synchronize do
        prune_expired_extension_ui_dialogs
        @pending_extension_ui_dialogs[id]&.tap { |pending| pending[:answering] = true }
      end
      return { "type" => "response", "command" => "extension_ui_response", "success" => false, "error" => "Extension UI request is no longer pending" } unless dialog

      begin
        write_command_unlocked(command, deadline: deadline)
        @mutex.synchronize { @pending_extension_ui_dialogs.delete(id) }
      rescue Errno::EPIPE, IOError
        @mutex.synchronize do
          if @pending_extension_ui_dialogs[id].equal?(dialog)
            dialog[:answering] = false
            @event_sequence += 1
            append_replay_event(dialog[:event], JSON.generate(dialog[:event]).bytesize)
            @condition.broadcast
          end
        end
        raise
      end
    end
    { "type" => "response", "command" => "extension_ui_response", "success" => true }
  end

  private

  def write_command(command, deadline: nil, type: command[:type])
    with_write_lock(deadline, type) do
      write_command_unlocked(command, deadline: deadline, type: type)
      yield if block_given?
    end
  end

  def with_write_lock(deadline, type)
    acquired = false
    until (acquired = @write_mutex.try_lock)
      raise RequestTimeout, "Pi RPC command timed out: #{type}" if deadline && remaining_timeout(deadline).zero?

      sleep [remaining_timeout(deadline) || 0.01, 0.01].min
    end
    yield
  ensure
    @write_mutex.unlock if acquired
  end

  def write_command_unlocked(command, deadline: nil, type: command[:type])
    serialized = JSON.generate(command) + "\n"
    unless deadline && @stdin.respond_to?(:write_nonblock)
      @stdin.write(serialized)
      @stdin.flush if @stdin.respond_to?(:flush)
      return
    end

    offset = 0
    while offset < serialized.bytesize
      written = @stdin.write_nonblock(serialized.byteslice(offset..), exception: false)
      if written == :wait_writable
        wait = remaining_timeout(deadline)
        raise RequestTimeout, "Pi RPC command timed out: #{type}" unless wait&.positive? && IO.select(nil, [@stdin], nil, wait)
      else
        offset += written
      end
    end
  end

  def queue_compaction_follow_up_payload(payload)
    @mutex.synchronize do
      return false unless @compacting || @flushing_compaction_follow_ups

      @compaction_follow_ups << payload
      true
    end
  end

  def wait_for_compaction_follow_up_flush(deadline = nil)
    @mutex.synchronize do
      while @flushing_compaction_follow_ups || (@compacting && @compaction_follow_ups.any?)
        raise RequestTimeout, "Pi RPC command timed out: prompt" if deadline && remaining_timeout(deadline).zero?

        @condition.wait(@mutex, [remaining_timeout(deadline) || 0.1, 0.1].min)
      end
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
    if response["type"] == "agent_end"
      @oversized_tool_update_sampled_at.clear
    elsif response["type"] == "tool_execution_end"
      @oversized_tool_update_sampled_at.delete(response["toolCallId"])
    end

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
          serialized_bytesize = JSON.generate(response).bytesize
        end
        update_busy_state(response)
        update_queued_messages(response)
        update_extension_ui_state(response)
        if ["compaction", "compaction_end"].include?(response["type"])
          follow_ups_to_flush = begin_compaction_follow_up_flush
          first_follow_up_type = "follow_up" if response["type"] == "compaction_end" && response["willRetry"] == true
        end
        update_active_tool_events(response, serialized_bytesize)
        discard_superseded_replay_updates(response)
        @event_sequence += 1
        replay_event, replay_bytes = bounded_replay_event(response, serialized_bytesize)
        if replay_event
          append_replay_event(replay_event, replay_bytes, coalesce_key: replay_coalesce_key(response))
        else
          discard_replay_buffer
        end
      end
      @condition.broadcast
    end
    flush_compaction_follow_ups(follow_ups_to_flush, first_follow_up_type) if follow_ups_to_flush&.any?
  end

  def update_queued_messages(response)
    return unless response["type"] == "queue_update"

    @queued_messages = {
      "steering" => Array(response["steering"]).select { |message| message.is_a?(String) },
      "followUp" => Array(response["followUp"]).select { |message| message.is_a?(String) }
    }
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

  def discard_superseded_replay_updates(response)
    case response["type"]
    when "message_start", "message_end"
      remove_coalesced_replay_event(:message_update)
    when "tool_execution_start", "tool_execution_end"
      remove_coalesced_replay_event(tool_replay_key(response["toolCallId"]))
    when "agent_end"
      @coalesced_events.keys.each { |key| remove_coalesced_replay_event(key) }
    end
  end

  def replay_coalesce_key(response)
    case response["type"]
    when "message_update"
      :message_update
    when "tool_execution_update"
      tool_replay_key(response["toolCallId"])
    end
  end

  def tool_replay_key(tool_call_id)
    return unless tool_call_id.is_a?(String) && tool_call_id.bytesize <= MAX_ACTIVE_TOOL_SNAPSHOT_ID_BYTES

    [:tool_update, tool_call_id]
  end

  def bounded_replay_event(response, serialized_bytesize)
    return [response, serialized_bytesize] unless response["type"] == "tool_execution_update" && response["toolName"] == SNAPSHOT_TOOL_NAME

    event = bounded_active_tool_event(response, serialized_bytesize)
    [event, event.equal?(response) ? serialized_bytesize : JSON.generate(event).bytesize]
  end

  def append_replay_event(event, bytes, coalesce_key: nil)
    remove_coalesced_replay_event(coalesce_key) if coalesce_key
    entry = [@event_sequence, event, bytes, coalesce_key]
    @events << entry
    @event_buffer_bytesize += bytes
    @coalesced_events[coalesce_key] = entry if coalesce_key
    prune_replay_events
  end

  def remove_coalesced_replay_event(key)
    return unless key

    entry = @coalesced_events.delete(key)
    return unless entry && @events.delete(entry)

    @event_buffer_bytesize -= entry.fetch(2)
  end

  def prune_replay_events
    while @events.length > @event_buffer_limit || @event_buffer_bytesize > @event_buffer_bytes
      sequence, _event, bytes, coalesce_key = @events.shift
      @event_buffer_bytesize -= bytes
      @event_replay_floor = [@event_replay_floor, sequence].max
      @coalesced_events.delete(coalesce_key) if coalesce_key
    end
  end

  def discard_replay_buffer
    @events.clear
    @coalesced_events.clear
    @event_buffer_bytesize = 0
    @event_replay_floor = @event_sequence
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

  def begin_bash(bash_id, command, exclude_from_context, started_at)
    token = { id: bash_id, command: command, exclude_from_context: !!exclude_from_context, started: false, started_at: started_at }
    @mutex.synchronize do
      raise BashAlreadyRunning, "A bash command is already running for this Pi RPC client" if @active_bash_token

      @active_bash_token = token
    end
    token
  end

  def start_bash(token)
    @mutex.synchronize do
      return if token[:started]

      token[:started] = true
      started_at = token.fetch(:started_at)
      if @active_bash_token.equal?(token)
        @active_bash_id = token.fetch(:id)
        @active_bash_command = token.fetch(:command)
        @active_bash_exclude_from_context = token.fetch(:exclude_from_context)
        @active_bash_started_at = started_at
      end
      append_gateway_event(
        "type" => "bash_start",
        "bashId" => token.fetch(:id),
        "command" => token.fetch(:command),
        "excludeFromContext" => token.fetch(:exclude_from_context),
        "gatewayTimestamp" => (started_at.to_f * 1000).to_i
      )
    end
  end

  def complete_bash(token, type, value_key, value, command: nil, exclude_from_context: nil)
    @mutex.synchronize do
      active = @active_bash_token.equal?(token)
      command ||= @active_bash_command if active
      exclude_from_context = @active_bash_exclude_from_context if active
      bash_id = active ? @active_bash_id : token.fetch(:id)
      clear_active_bash_state if active
      event = {
        "type" => type,
        "bashId" => bash_id,
        "command" => command,
        "excludeFromContext" => !!exclude_from_context,
        value_key => value,
        "startedAt" => (token.fetch(:started_at).to_f * 1000).to_i,
        "gatewayTimestamp" => (@clock.call.to_f * 1000).to_i
      }
      if active && @agent_running
        @completed_bash_events[bash_id] = event
        @completed_bash_events.shift while @completed_bash_events.length > MAX_COMPLETED_BASH_EVENTS
      end
      append_gateway_event(event)
    end
  end

  def clear_active_bash_state
    @active_bash_token = nil
    @active_bash_id = nil
    @active_bash_command = nil
    @active_bash_exclude_from_context = false
    @active_bash_started_at = nil
    @condition.broadcast
  end

  def append_gateway_event(event)
    @event_sequence += 1
    append_replay_event(event, JSON.generate(event).bytesize)
    @condition.broadcast
  end

  def busy_locked?
    @busy || !!@active_bash_token
  end

  def busy_since_locked
    [@busy_since, @active_bash_started_at].compact.min
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
      @completed_bash_events.clear
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

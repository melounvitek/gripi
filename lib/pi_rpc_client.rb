require "json"
require "open3"
require "securerandom"
require "thread"

class PiRpcClient
  DEFAULT_EVENT_BUFFER_LIMIT = 5_000
  GATEWAY_EXTENSION_PATH = File.expand_path("../pi_extensions/pi-web-gateway-tree.ts", __dir__)
  RUBY_ENV_KEYS = %w[
    GEM_HOME
    GEM_PATH
    RUBYLIB
    RUBYOPT
  ].freeze

  def self.start(session_path, command_prefix: ["pi"], popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GATEWAY_EXTENSION_PATH, "--session", session_path)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def self.start_in_cwd(cwd, command_prefix: ["pi"], popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call(pi_process_env, *command_prefix, "--mode", "rpc", "--extension", GATEWAY_EXTENSION_PATH, chdir: cwd)
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
      raise ArgumentError, "PI_GATEWAY_NODE and PI_GATEWAY_PI must be set together to pin Pi to a specific Node runtime. Set both, or unset both to run pi from PATH."
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
    @event_buffer_limit = event_buffer_limit
    @clock = clock
    @mutex = Mutex.new
    @condition = ConditionVariable.new
    @reader_running = false
    @busy = false
    @busy_since = nil
    @agent_running = false
    @compacting = false
    @reader = nil
  end

  def get_state
    request("get_state", id: next_id("get_state"))
  end

  def get_messages
    request("get_messages", id: next_id("get_messages"))
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
    @mutex.synchronize { mark_compacting }
    response = request("compact", id: next_id("compact"), **payload)
    @mutex.synchronize { clear_compacting } if !response || response["success"] == false
    response
  rescue StandardError
    @mutex.synchronize { clear_compacting }
    raise
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
    response = request("prompt", id: next_id("prompt"), message: "/pi_web_tree #{entry_id} #{request_id}")
    return response unless response&.fetch("success", true)

    result = wait_for_status("pi_web_tree:#{request_id}")
    return response unless result

    response.merge("data" => { "cancelled" => result == "cancelled" })
  end

  def tree_leaf
    request_id = SecureRandom.hex(8)
    response = request("prompt", id: next_id("prompt"), message: "/pi_web_tree_leaf #{request_id}")
    return unless response&.fetch("success", true)

    result = wait_for_status("pi_web_tree_leaf:#{request_id}")
    result == "__root__" ? nil : result
  end

  def set_session_name(name)
    request("set_session_name", id: next_id("set_session_name"), name: name)
  end

  def event_sequence
    @mutex.synchronize { @event_sequence }
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
      @stdin.write(JSON.generate(command) + "\n")
      @stdin.flush if @stdin.respond_to?(:flush)
    rescue Errno::EPIPE
      @mutex.synchronize { @pending_ids.delete(id) }
      raise IOError, "Pi RPC process exited before accepting command"
    end

    @mutex.synchronize do
      loop do
        return @responses.delete(id) if @responses.key?(id)
        unless @reader_running
          @pending_ids.delete(id)
          return nil
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
        return nil unless @reader_running

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
    @request_sequence += 1
    "#{type}-#{@request_sequence}"
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
        store_response(JSON.parse(line))
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
      @busy = false
      @busy_since = nil
      @condition.broadcast
    end
  end

  def store_response(response)
    @mutex.synchronize do
      if response["id"] && @pending_ids.delete(response["id"])
        @responses[response["id"]] = response
      else
        update_busy_state(response)
        @event_sequence += 1
        @events << [@event_sequence, response]
        @events.shift while @events.length > @event_buffer_limit
      end
      @condition.broadcast
    end
  end

  def update_busy_state(response)
    case response["type"]
    when "agent_start"
      @agent_running = true
      @busy = true
      @busy_since ||= @clock.call
    when "compaction_start"
      mark_compacting
    when "turn_start"
      @busy = true
      @busy_since ||= @clock.call
    when "turn_end"
      clear_busy_state unless @agent_running
    when "agent_end"
      @agent_running = false
      clear_busy_state
    when "compaction", "compaction_end"
      clear_compacting
    end
  end

  def mark_compacting
    @compacting = true
    @busy = true
    @busy_since ||= @clock.call
  end

  def clear_compacting
    @compacting = false
    clear_busy_state unless @agent_running
  end

  def clear_busy_state
    @busy = false
    @busy_since = nil
  end
end

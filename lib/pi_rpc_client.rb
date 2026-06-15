require "json"
require "open3"
require "thread"

class PiRpcClient
  DEFAULT_EVENT_BUFFER_LIMIT = 5_000

  def self.start(session_path, popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call("pi", "--mode", "rpc", "--session", session_path)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def self.start_in_cwd(cwd, popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call("pi", "--mode", "rpc", chdir: cwd)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def initialize(stdin:, stdout:, stderr: nil, wait_thread: nil, event_buffer_limit: DEFAULT_EVENT_BUFFER_LIMIT)
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
    @mutex = Mutex.new
    @condition = ConditionVariable.new
    @reader_running = false
    @busy = false
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

  def set_session_name(name)
    request("set_session_name", id: next_id("set_session_name"), name: name)
  end

  def event_sequence
    @mutex.synchronize { @event_sequence }
  end

  def busy?
    @mutex.synchronize { @busy }
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
    @stdin.write(JSON.generate(command) + "\n")
    @stdin.flush if @stdin.respond_to?(:flush)

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
      @busy = false
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
    when "agent_start", "turn_start"
      @busy = true
    when "agent_end", "turn_end"
      @busy = false
    end
  end
end

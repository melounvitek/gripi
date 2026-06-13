require "json"
require "open3"
require "thread"

class PiRpcClient
  def self.start(session_path, popen: Open3.method(:popen3))
    stdin, stdout, stderr, wait_thread = popen.call("pi", "--mode", "rpc", "--session", session_path)
    new(stdin: stdin, stdout: stdout, stderr: stderr, wait_thread: wait_thread)
  end

  def initialize(stdin:, stdout:, stderr: nil, wait_thread: nil)
    @stdin = stdin
    @stdout = stdout
    @stderr = stderr
    @wait_thread = wait_thread
    @request_sequence = 0
    @responses = {}
    @pending_ids = {}
    @events = []
    @mutex = Mutex.new
    @condition = ConditionVariable.new
    @reader_running = false
    @reader = nil
  end

  def get_state
    request("get_state", id: next_id("get_state"))
  end

  def get_messages
    request("get_messages", id: next_id("get_messages"))
  end

  def prompt(message)
    request("prompt", id: next_id("prompt"), message: message)
  end

  def abort
    request("abort", id: next_id("abort"))
  end

  def drain_events
    ensure_reader
    @mutex.synchronize do
      events = @events
      @events = []
      events
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
      @condition.broadcast
    end
  end

  def store_response(response)
    @mutex.synchronize do
      if response["id"] && @pending_ids.delete(response["id"])
        @responses[response["id"]] = response
      else
        @events << response
      end
      @condition.broadcast
    end
  end
end

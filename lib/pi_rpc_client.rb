require "json"
require "open3"

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
  end

  def request(type, id:, **payload)
    command = payload.merge(id: id, type: type)
    @stdin.write(JSON.generate(command) + "\n")
    @stdin.flush if @stdin.respond_to?(:flush)

    each_response do |response|
      return response if response["id"] == id
    end

    nil
  end

  private

  def each_response
    while (line = @stdout.gets)
      next if line.strip.empty?

      begin
        yield JSON.parse(line)
      rescue JSON::ParserError
        next
      end
    end
  end
end

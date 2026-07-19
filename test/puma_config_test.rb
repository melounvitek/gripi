require "fileutils"
require "minitest/autorun"
require "open3"
require "puma"
require "puma/configuration"
require "puma/server"
require "socket"
require "timeout"
require "tmpdir"

class PumaConfigTest < Minitest::Test
  PROJECT_ROOT = File.expand_path("..", __dir__)
  PRODUCTION_REQUEST_BODY_LIMIT = 64 * 1024 * 1024
  TEST_REQUEST_BODY_LIMIT = 1_024

  def setup
    @tmpdir = Dir.mktmpdir
    @app_calls_path = File.join(@tmpdir, "app-calls")
    @rackup_root = File.join(@tmpdir, "rackup")
    FileUtils.mkdir_p(File.join(@rackup_root, "config"))
    FileUtils.ln_s(File.join(PROJECT_ROOT, "lib"), File.join(@rackup_root, "lib"))
    production_config = File.read(File.join(PROJECT_ROOT, "config/puma.rb"))
    File.write(
      File.join(@rackup_root, "config/puma.rb"),
      production_config + "\nhttp_content_length_limit #{TEST_REQUEST_BODY_LIMIT}\n"
    )
    File.write(File.join(@rackup_root, "config.ru"), <<~RUBY)
      calls_path = #{@app_calls_path.inspect}
      run lambda { |_env|
        File.write(calls_path, "called")
        [200, { "content-type" => "text/plain" }, ["OK"]]
      }
    RUBY

    @port = available_port
    @stdin, @stdout, @stderr, @wait_thread = Open3.popen3(
      { "BUNDLE_GEMFILE" => File.join(PROJECT_ROOT, "Gemfile"), "RACK_ENV" => "production" },
      "bundle", "exec", "rackup", "--quiet", "-o", "127.0.0.1", "-p", @port.to_s,
      chdir: @rackup_root
    )
    @stdin.close
    wait_until_listening
  end

  def teardown
    if @wait_thread&.alive?
      Process.kill("TERM", @wait_thread.pid)
      unless @wait_thread.join(5)
        Process.kill("KILL", @wait_thread.pid)
        @wait_thread.join
      end
    end
    @stdout&.close
    @stderr&.close
    FileUtils.remove_entry(@tmpdir) if @tmpdir && Dir.exist?(@tmpdir)
  end

  def test_rejects_oversized_content_length_before_calling_the_rack_app
    configuration = Puma::Configuration.new(config_files: [File.join(PROJECT_ROOT, "config/puma.rb")])
    configuration.load
    assert_equal PRODUCTION_REQUEST_BODY_LIMIT, configuration.options[:http_content_length_limit]

    response = TCPSocket.open("127.0.0.1", @port) do |socket|
      content_length = TEST_REQUEST_BODY_LIMIT + 1
      socket.write(<<~HTTP.gsub("\n", "\r\n") + ("x" * content_length) + <<~HTTP.gsub("\n", "\r\n"))
        POST / HTTP/1.1
        Host: 127.0.0.1
        Content-Length: #{content_length}
        Expect: 100-continue

      HTTP
        GET /smuggled HTTP/1.1
        Host: 127.0.0.1
        Connection: close

      HTTP

      response_buffer = +""
      response = read_response(socket, response_buffer)
      response = read_response(socket, response_buffer) if response.match?(/\AHTTP\/1\.1 100 /)
      assert_equal "", response_buffer
      assert_equal "", Timeout.timeout(5) { socket.read }
      response
    end

    assert_match(/\AHTTP\/1\.1 413 /, response)
    refute File.exist?(@app_calls_path)
  end

  def test_body_limit_patch_can_be_required_independently
    _output, error, status = Open3.capture3(
      { "BUNDLE_GEMFILE" => File.join(PROJECT_ROOT, "Gemfile") },
      "bundle", "exec", "ruby", "-Ilib", "-e", 'require "puma_chunked_body_limit"',
      chdir: PROJECT_ROOT
    )

    assert status.success?, error
  end

  def test_rejects_chunked_body_over_the_limit_without_waiting_for_the_terminal_chunk
    response = TCPSocket.open("127.0.0.1", @port) do |socket|
      socket.write(<<~HTTP.gsub("\n", "\r\n"))
        POST / HTTP/1.1
        Host: 127.0.0.1
        Transfer-Encoding: chunked
        Expect: 100-continue

      HTTP
      response_buffer = +""
      assert_match(/\AHTTP\/1\.1 100 /, read_response(socket, response_buffer))

      2.times do
        body = "x" * 600
        socket.write("#{body.bytesize.to_s(16)}\r\n#{body}\r\n")
      end
      response = read_response(socket, response_buffer)
      assert_equal "", response_buffer
      assert_equal "", Timeout.timeout(5) { socket.read }
      response
    end

    assert_match(/\AHTTP\/1\.1 413 /, response)
    refute File.exist?(@app_calls_path)
  end

  private

  def read_response(socket, buffer)
    Timeout.timeout(5) do
      buffer << socket.readpartial(1024) until buffer.include?("\r\n\r\n")
      headers = buffer.slice!(0, buffer.index("\r\n\r\n") + 4)
      content_length = headers[/\r\nContent-Length: (\d+)/i, 1].to_i
      buffer << socket.readpartial(1024) while buffer.bytesize < content_length
      headers + buffer.slice!(0, content_length)
    end
  end

  def available_port
    server = TCPServer.new("127.0.0.1", 0)
    server.local_address.ip_port
  ensure
    server&.close
  end

  def wait_until_listening
    Timeout.timeout(10) do
      loop do
        TCPSocket.open("127.0.0.1", @port).close
        break
      rescue Errno::ECONNREFUSED
        raise "Puma exited before accepting connections: #{@stderr.read}" unless @wait_thread.alive?
        sleep 0.05
      end
    end
  end
end

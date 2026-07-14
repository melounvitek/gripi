require "fileutils"
require "minitest/autorun"
require "tmpdir"
require_relative "../lib/request_gateway_restart"

class RequestGatewayRestartTest < Minitest::Test
  def setup
    @tmpdir = Dir.mktmpdir
    @restart_path = File.join(@tmpdir, "state", "restart-request")
  end

  def teardown
    FileUtils.remove_entry(@tmpdir) if @tmpdir
  end

  def test_creates_marker_then_closes_clients_then_shuts_down
    calls = []
    registry = Object.new
    registry.define_singleton_method(:close_all) do
      calls << [:close, File.exist?(@restart_path)]
    end
    registry.instance_variable_set(:@restart_path, @restart_path)
    requester = RequestGatewayRestart.new(shutdown: -> { calls << [:shutdown, File.exist?(@restart_path)] })

    requester.call(registry, env: { "GRIPI_RESTART_PATH" => @restart_path })

    assert_equal [[:close, true], [:shutdown, true]], calls
    assert File.file?(@restart_path)
    assert_empty Dir.glob("#{@restart_path}.tmp-*")
  end

  def test_shuts_down_when_no_rpc_registry_exists
    shutdowns = 0
    requester = RequestGatewayRestart.new(shutdown: -> { shutdowns += 1 })

    requester.call(nil, env: { "GRIPI_RESTART_PATH" => @restart_path })

    assert_equal 1, shutdowns
    assert File.file?(@restart_path)
  end

  def test_does_not_close_clients_or_shut_down_when_marker_creation_fails
    calls = []
    requester = RequestGatewayRestart.new(shutdown: -> { calls << :shutdown })
    registry = Object.new
    registry.define_singleton_method(:close_all) { calls << :close }

    assert_raises(Errno::EEXIST) do
      requester.call(registry, env: { "GRIPI_RESTART_PATH" => File.join(__FILE__, "restart-request") })
    end
    assert_empty calls
  end

  def test_still_shuts_down_when_closing_clients_fails
    calls = []
    registry = Object.new
    registry.define_singleton_method(:close_all) do
      calls << :close
      raise "close failed"
    end
    requester = RequestGatewayRestart.new(shutdown: -> { calls << :shutdown })

    error = assert_raises(RuntimeError) do
      requester.call(registry, env: { "GRIPI_RESTART_PATH" => @restart_path })
    end

    assert_equal "close failed", error.message
    assert_equal [:close, :shutdown], calls
    assert File.file?(@restart_path)
  end

  def test_removes_marker_when_shutdown_fails
    requester = RequestGatewayRestart.new(shutdown: -> { raise "shutdown failed" })

    error = assert_raises(RuntimeError) do
      requester.call(nil, env: { "GRIPI_RESTART_PATH" => @restart_path })
    end

    assert_equal "shutdown failed", error.message
    refute File.exist?(@restart_path)
  end

  def test_default_path_uses_home_and_reports_a_missing_home
    requester = RequestGatewayRestart.new(shutdown: -> {})
    home = File.join(@tmpdir, "home")

    requester.call(nil, env: { "HOME" => home })

    assert File.file?(File.join(home, ".pi", "gripi", "restart-request"))
    error = assert_raises(ArgumentError) { requester.call(nil, env: {}) }
    assert_match(/HOME|GRIPI_RESTART_PATH/, error.message)
  end
end

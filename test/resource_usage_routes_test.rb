ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "json"
require "tmpdir"
require "fileutils"
require_relative "../app"

class ResourceUsageRoutesTest < Minitest::Test
  class FakeMonitor
    attr_reader :calls

    def initialize(snapshot)
      @snapshot = snapshot
      @calls = 0
    end

    def snapshot
      @calls += 1
      @snapshot
    end
  end

  class CleanupRejectingRegistry
    attr_reader :cleanup_calls

    def initialize
      @cleanup_calls = 0
    end

    def close_idle_clients(**)
      @cleanup_calls += 1
      raise "resource polling must not clean up RPC clients"
    end
  end

  def setup
    @sessions_root = Dir.mktmpdir
    Gripi.set :sessions_root, @sessions_root
    Gripi.set :browser_auth_disabled, true
    Gripi.set :multi_user_mode, false
    Gripi.set :resource_monitoring_enabled, true
    @monitor = FakeMonitor.new(
      memory_bytes: 637_181_952,
      working_set_bytes: 502_964_224,
      inactive_file_bytes: 134_217_728,
      cpu_usage_usec: 1_234_567,
      puma_rss_bytes: 371_124 * 1024,
      pi_rss_bytes: 365_852 * 1024,
      pi_process_count: 2
    )
    Gripi.set :resource_usage_monitor, @monitor
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    Gripi.set :resource_monitoring_enabled, false
    Gripi.set :rpc_client_registry, nil
    FileUtils.remove_entry(@sessions_root)
  end

  def test_returns_aggregate_resource_usage
    response = @request.get("/resource-usage")

    assert_equal 200, response.status
    assert_equal "application/json", response.media_type
    assert_includes response.headers.fetch("cache-control"), "no-store"
    assert_equal(
      {
        "supported" => true,
        "memoryBytes" => 637_181_952,
        "workingSetBytes" => 502_964_224,
        "inactiveFileBytes" => 134_217_728,
        "cpuUsageUsec" => 1_234_567,
        "pumaRssBytes" => 371_124 * 1024,
        "piRssBytes" => 365_852 * 1024,
        "piProcessCount" => 2
      },
      JSON.parse(response.body)
    )
  end

  def test_returns_not_found_without_server_opt_in
    Gripi.set :resource_monitoring_enabled, false

    response = @request.get("/resource-usage")

    assert_equal 404, response.status
    assert_equal 0, @monitor.calls
  end

  def test_reports_an_unsupported_linux_environment
    Gripi.set :resource_usage_monitor, FakeMonitor.new(nil)

    response = @request.get("/resource-usage")

    assert_equal 200, response.status
    assert_equal({ "supported" => false }, JSON.parse(response.body))
  end

  def test_sidebar_indicator_is_rendered_only_with_server_opt_in
    enabled_response = @request.get("/")
    Gripi.set :resource_monitoring_enabled, false
    disabled_response = @request.get("/")

    assert_equal 200, enabled_response.status
    assert_includes enabled_response.body, "data-resource-usage"
    assert_equal 200, disabled_response.status
    refute_includes disabled_response.body, "data-resource-usage"
  end

  def test_polling_does_not_trigger_rpc_idle_cleanup
    registry = CleanupRejectingRegistry.new
    Gripi.set :rpc_client_registry, registry
    Gripi.set :rpc_idle_timeout_seconds, 1

    response = @request.get("/resource-usage")

    assert_equal 200, response.status
    assert_equal 0, registry.cleanup_calls
  ensure
    Gripi.set :rpc_idle_timeout_seconds, 0
  end
end

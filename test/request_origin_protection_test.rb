ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"
ENV["GRIPI_PERMITTED_HOSTS"] ||= "gateway.tailnet.ts.net"

require "minitest/autorun"
require "rack/mock"
require "json"
require "tmpdir"
require "fileutils"
require_relative "../app"

class RequestOriginProtectionTest < Minitest::Test
  Snapshot = Struct.new(:state, :reason, :message, :current_sha, :target_sha, :behind_count, :summary, :active_session_count, keyword_init: true)

  class FakeCoordinator
    attr_reader :start_calls, :status_calls

    def initialize
      @start_calls = 0
      @status_calls = 0
    end

    def cached_status
      Snapshot.new(state: :available, message: "Update available", target_sha: "target")
    end

    def status
      @status_calls += 1
      Snapshot.new(state: :available, message: "Update available", target_sha: "target")
    end

    def start
      @start_calls += 1
      Snapshot.new(state: :updating, message: "Updating gateway…", target_sha: "target")
    end
  end

  def setup
    @sessions_root = Dir.mktmpdir
    @browser_access_root = Dir.mktmpdir
    @workspace_root = Dir.mktmpdir
    @coordinator = FakeCoordinator.new
    Gripi.set :sessions_root, @sessions_root
    Gripi.set :browser_access_path, File.join(@browser_access_root, "browser-access.json")
    Gripi.set :browser_auth_disabled, true
    Gripi.set :multi_user_mode, false
    Gripi.set :workspace_secret_path, File.join(@workspace_root, "workspace-secret")
    Gripi.set :workspace_access_path, File.join(@workspace_root, "workspace-access.json")
    Gripi.set :workspace_ownership_path, File.join(@workspace_root, "session-owners.json")
    Gripi.set :gateway_update_coordinator, @coordinator
    Gripi.set :gateway_instance_id, "instance1"
    Gripi.set :rpc_idle_timeout_seconds, 0
    Gripi.set :trust_proxy_headers, false
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    FileUtils.remove_entry(@sessions_root) if @sessions_root && Dir.exist?(@sessions_root)
    FileUtils.remove_entry(@browser_access_root) if @browser_access_root && Dir.exist?(@browser_access_root)
    FileUtils.remove_entry(@workspace_root) if @workspace_root && Dir.exist?(@workspace_root)
  end

  def test_allows_unsafe_request_with_same_origin
    response = @request.post("/gateway-update", "HTTP_ORIGIN" => "http://example.org")

    assert_equal 202, response.status
    assert_equal "updating", JSON.parse(response.body).fetch("state")
    assert_equal 1, @coordinator.start_calls
  end

  def test_rejects_unsafe_request_with_cross_origin
    response = @request.post("/gateway-update", "HTTP_ORIGIN" => "http://evil.example")

    assert_equal 403, response.status
    assert_equal 0, @coordinator.start_calls
  end

  def test_rejects_update_check_with_cross_origin
    response = @request.post("/gateway-update/check", "HTTP_ORIGIN" => "http://evil.example")

    assert_equal 403, response.status
    assert_equal 0, @coordinator.status_calls
  end

  def test_rejects_unsafe_request_with_cross_site_fetch_metadata
    response = @request.post("/gateway-update", "HTTP_SEC_FETCH_SITE" => "cross-site")

    assert_equal 403, response.status
    assert_equal 0, @coordinator.start_calls
  end

  def test_allows_unsafe_request_without_browser_origin_headers
    response = @request.post("/gateway-update")

    assert_equal 202, response.status
    assert_equal 1, @coordinator.start_calls
  end

  def test_allows_unsafe_request_with_same_origin_referer_when_origin_is_absent
    response = @request.post("/gateway-update", "HTTP_REFERER" => "http://example.org/settings")

    assert_equal 202, response.status
    assert_equal 1, @coordinator.start_calls
  end

  def test_rejects_unsafe_request_with_cross_origin_referer_when_origin_is_absent
    response = @request.post("/gateway-update", "HTTP_REFERER" => "http://evil.example/attack")

    assert_equal 403, response.status
    assert_equal 0, @coordinator.start_calls
  end

  def test_does_not_apply_to_get_requests
    response = @request.get("/gateway-update", "HTTP_ORIGIN" => "http://evil.example", "HTTP_SEC_FETCH_SITE" => "cross-site")

    assert_equal 200, response.status
    assert_equal 0, @coordinator.status_calls
  end

  def test_rejects_spoofed_forwarded_host_when_proxy_headers_are_not_trusted
    response = @request.post(
      "/gateway-update",
      "HTTP_HOST" => "gateway.tailnet.ts.net",
      "HTTP_X_FORWARDED_HOST" => "proxy.test",
      "HTTP_X_FORWARDED_PROTO" => "https",
      "HTTP_ORIGIN" => "https://proxy.test"
    )

    assert_equal 403, response.status
    assert_includes response.body, "GRIPI_TRUST_PROXY_HEADERS=1"
    assert_includes response.body, "systemctl --user restart gripi.service"
    assert_equal 0, @coordinator.start_calls
  end

  def test_allows_forwarded_https_origin_when_proxy_headers_are_trusted
    Gripi.set :trust_proxy_headers, true

    response = @request.post(
      "/gateway-update",
      "HTTP_HOST" => "gateway.tailnet.ts.net",
      "HTTP_X_FORWARDED_PROTO" => "https",
      "HTTP_ORIGIN" => "https://gateway.tailnet.ts.net"
    )

    assert_equal 202, response.status
    assert_equal 1, @coordinator.start_calls
  end

  def test_allows_forwarded_host_and_port_origin_when_proxy_headers_are_trusted
    Gripi.set :trust_proxy_headers, true

    response = @request.post(
      "/gateway-update",
      "HTTP_HOST" => "127.0.0.1:4567",
      "HTTP_X_FORWARDED_HOST" => "gateway.tailnet.ts.net",
      "HTTP_X_FORWARDED_PROTO" => "https",
      "HTTP_X_FORWARDED_PORT" => "443",
      "HTTP_ORIGIN" => "https://gateway.tailnet.ts.net"
    )

    assert_equal 202, response.status
    assert_equal 1, @coordinator.start_calls
  end

  def test_rejects_cross_origin_even_when_trusted_forwarded_headers_are_present
    Gripi.set :trust_proxy_headers, true

    response = @request.post(
      "/gateway-update",
      "HTTP_HOST" => "gateway.tailnet.ts.net",
      "HTTP_X_FORWARDED_PROTO" => "https",
      "HTTP_ORIGIN" => "https://evil.example"
    )

    assert_equal 403, response.status
    assert_equal 0, @coordinator.start_calls
  end

  def test_rejects_cross_origin_browser_access_approval_before_route_handler
    Gripi.set :browser_auth_disabled, false
    Gripi.set :gateway_admin_password, "secret"
    BrowserAccessStore.new(path: Gripi.settings.browser_access_path).approve_current_browser("approved-token", label: "test")

    response = @request.post(
      "/browser-access/approve",
      params: { "code" => "CODE1" },
      "HTTP_COOKIE" => "gripi_browser=approved-token",
      "HTTP_ORIGIN" => "http://evil.example"
    )

    assert_equal 403, response.status
  ensure
    Gripi.set :browser_auth_disabled, true
  end

  def test_rejects_cross_origin_workspace_access_approval_before_route_handler
    Gripi.set :multi_user_mode, true
    store = WorkspaceAccessStore.new(path: Gripi.settings.workspace_access_path)
    workspace_id = "workspace1"
    store.approve_workspace(workspace_id)

    response = @request.post(
      "/workspace-access/approve",
      params: { "code" => "CODE1" },
      "HTTP_COOKIE" => "gripi_workspace=#{workspace_id}",
      "HTTP_ORIGIN" => "http://evil.example"
    )

    assert_equal 403, response.status
  ensure
    Gripi.set :multi_user_mode, false
  end
end

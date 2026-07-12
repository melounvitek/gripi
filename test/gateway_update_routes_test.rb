ENV["PI_GATEWAY_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "json"
require "tmpdir"
require "fileutils"
require_relative "../app"

class GatewayUpdateRoutesTest < Minitest::Test
  Snapshot = Struct.new(:state, :reason, :message, :current_sha, :target_sha, :behind_count, :summary, keyword_init: true)

  class FakeCoordinator
    attr_accessor :snapshot
    attr_reader :status_calls, :start_calls

    def initialize(snapshot)
      @snapshot = snapshot
      @status_calls = 0
      @start_calls = 0
    end

    def status
      @status_calls += 1
      snapshot
    end

    def start
      @start_calls += 1
      self.snapshot = Snapshot.new(state: :updating, target_sha: snapshot.target_sha, message: "Updating gateway…")
    end
  end

  def setup
    @sessions_root = Dir.mktmpdir
    @coordinator = FakeCoordinator.new(
      Snapshot.new(
        state: :available,
        message: "2 updates available",
        current_sha: "current1",
        target_sha: "target22",
        behind_count: 2,
        summary: "target22 Improve updates"
      )
    )
    PiWebGateway.set :sessions_root, @sessions_root
    PiWebGateway.set :browser_auth_disabled, true
    PiWebGateway.set :multi_user_mode, false
    PiWebGateway.set :rpc_idle_timeout_seconds, 0
    PiWebGateway.set :gateway_instance_id, "instance1"
    PiWebGateway.set :gateway_update_coordinator, @coordinator
    @request = Rack::MockRequest.new(PiWebGateway)
  end

  def teardown
    FileUtils.remove_entry(@sessions_root) if Dir.exist?(@sessions_root)
  end

  def test_get_returns_the_coordinator_status_as_json
    response = @request.get("/gateway-update")

    assert_equal 200, response.status
    assert_equal "application/json", response.media_type
    assert_equal(
      {
        "instanceId" => "instance1",
        "state" => "available",
        "reason" => nil,
        "message" => "2 updates available",
        "currentSha" => "current1",
        "targetSha" => "target22",
        "behindCount" => 2,
        "summary" => "target22 Improve updates"
      },
      JSON.parse(response.body)
    )
    assert_equal 1, @coordinator.status_calls
  end

  def test_post_starts_the_update_and_returns_promptly_accepted_snapshot
    response = @request.post("/gateway-update")

    assert_equal 202, response.status
    assert_equal "updating", JSON.parse(response.body).fetch("state")
    assert_equal 1, @coordinator.start_calls
  end

  def test_existing_browser_access_gate_rejects_update_requests
    PiWebGateway.set :browser_auth_disabled, false
    PiWebGateway.set :gateway_admin_password, "secret"

    get_response = @request.get("/gateway-update")
    post_response = @request.post("/gateway-update")

    assert_equal 403, get_response.status
    assert_equal 403, post_response.status
    assert_equal 0, @coordinator.status_calls
    assert_equal 0, @coordinator.start_calls
  ensure
    PiWebGateway.set :browser_auth_disabled, true
  end

  def test_existing_workspace_access_gate_rejects_update_requests
    PiWebGateway.set :multi_user_mode, true

    get_response = @request.get("/gateway-update")
    post_response = @request.post("/gateway-update")

    assert_equal 403, get_response.status
    assert_equal 403, post_response.status
    assert_equal 0, @coordinator.status_calls
    assert_equal 0, @coordinator.start_calls
  ensure
    PiWebGateway.set :multi_user_mode, false
  end

  def test_page_rendering_does_not_check_update_status
    response = @request.get("/")

    assert_equal 200, response.status
    assert_equal 0, @coordinator.status_calls
  end

  def test_sidebar_contains_hidden_update_control_and_page_initializes_update_checks
    response = @request.get("/")
    document = Nokogiri::HTML(response.body)
    control = document.at_css("[data-gateway-update]")

    assert control
    assert control["hidden"]
    assert control.at_css("[data-gateway-update-button]")
    assert control.at_css("[data-gateway-update-message]")
    assert_includes response.body, 'fetch("/gateway-update"'
    assert_includes response.body, "confirm("
    assert_includes response.body, "setInterval(() => checkGatewayUpdate().catch(() => {}), 5 * 60 * 1000)"
    assert_includes response.body, "applyGatewayUpdateState(gatewayUpdateState)"
    assert_includes response.body, 'new BroadcastChannel("pi-gateway-update")'
    assert_includes response.body, 'gatewayUpdateChannel?.postMessage({ type: "updating" })'
    assert_includes response.body, 'payload.state !== "rollback_failed"'
  end

  def test_update_script_polls_restart_and_forces_a_cache_busted_full_navigation
    response = @request.get("/")

    assert_includes response.body, 'updateUrl.searchParams.set("_gateway_updated", targetSha)'
    assert_includes response.body, 'const cleanUrl = new URL(window.location.href)'
    assert_includes response.body, 'cleanUrl.searchParams.delete("_gateway_updated")'
    assert_includes response.body, "window.location.replace(updateUrl.href)"
    assert_includes response.body, "payload.instanceId !== gatewayInstanceId"
    assert_includes response.body, "gatewayUpdateNavigation(payload.currentSha || payload.instanceId)"
    assert_includes response.body, "pollGatewayUpdate"
    assert_includes response.body, 'window.addEventListener("focus", () => {'
    assert_includes response.body, "checkGatewayUpdate().catch(() => {})"
  end
end

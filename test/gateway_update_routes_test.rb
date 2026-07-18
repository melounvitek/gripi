ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "json"
require "tmpdir"
require "fileutils"
require_relative "../app"

class GatewayUpdateRoutesTest < Minitest::Test
  Snapshot = Struct.new(:state, :reason, :message, :current_sha, :target_sha, :behind_count, :summary, :active_session_count, keyword_init: true)

  class FakeCoordinator
    attr_accessor :snapshot
    attr_reader :status_calls, :start_calls

    def initialize(snapshot)
      @snapshot = snapshot
      @status_calls = 0
      @start_calls = 0
    end

    def cached_status
      snapshot
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
    @browser_access_root = Dir.mktmpdir
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
    Gripi.set :sessions_root, @sessions_root
    Gripi.set :browser_access_path, File.join(@browser_access_root, "browser-access.json")
    Gripi.set :browser_auth_disabled, true
    Gripi.set :multi_user_mode, false
    Gripi.set :rpc_idle_timeout_seconds, 0
    Gripi.set :gateway_instance_id, "instance1"
    Gripi.set :gateway_update_coordinator, @coordinator
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    FileUtils.remove_entry(@sessions_root) if Dir.exist?(@sessions_root)
    FileUtils.remove_entry(@browser_access_root) if Dir.exist?(@browser_access_root)
  end

  def test_get_returns_the_cached_coordinator_status_as_json
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
        "summary" => "target22 Improve updates",
        "activeSessionCount" => nil
      },
      JSON.parse(response.body)
    )
    assert_equal 0, @coordinator.status_calls
  end

  def test_post_check_refreshes_the_coordinator_status
    response = @request.post("/gateway-update/check")

    assert_equal 200, response.status
    assert_equal "available", JSON.parse(response.body).fetch("state")
    assert_equal 1, @coordinator.status_calls
  end

  def test_get_reports_sessions_delaying_an_update
    @coordinator.snapshot = Snapshot.new(
      state: :waiting,
      message: "Waiting for 2 active Pi sessions to finish…",
      active_session_count: 2
    )

    payload = JSON.parse(@request.get("/gateway-update").body)

    assert_equal "waiting", payload.fetch("state")
    assert_equal 2, payload.fetch("activeSessionCount")
  end

  def test_post_starts_the_update_and_returns_promptly_accepted_snapshot
    response = @request.post("/gateway-update")

    assert_equal 202, response.status
    assert_equal "updating", JSON.parse(response.body).fetch("state")
    assert_equal 1, @coordinator.start_calls
  end

  def test_existing_browser_access_gate_rejects_update_requests
    Gripi.set :browser_auth_disabled, false
    Gripi.set :gateway_admin_password, "secret"

    get_response = @request.get("/gateway-update")
    check_response = @request.post("/gateway-update/check")
    post_response = @request.post("/gateway-update")

    assert_equal 403, get_response.status
    assert_equal 403, check_response.status
    assert_equal 403, post_response.status
    assert_equal 0, @coordinator.status_calls
    assert_equal 0, @coordinator.start_calls
  ensure
    Gripi.set :browser_auth_disabled, true
  end

  def test_existing_workspace_access_gate_rejects_update_requests
    Gripi.set :multi_user_mode, true

    get_response = @request.get("/gateway-update")
    check_response = @request.post("/gateway-update/check")
    post_response = @request.post("/gateway-update")

    assert_equal 403, get_response.status
    assert_equal 403, check_response.status
    assert_equal 403, post_response.status
    assert_equal 0, @coordinator.status_calls
    assert_equal 0, @coordinator.start_calls
  ensure
    Gripi.set :multi_user_mode, false
  end

  def test_page_rendering_does_not_check_update_status
    response = @request.get("/")

    assert_equal 200, response.status
    assert_equal 0, @coordinator.status_calls
  end

  def test_sidebar_contains_hidden_update_control
    response = @request.get("/")
    document = Nokogiri::HTML(response.body)
    control = document.at_css("[data-gateway-update]")

    assert control
    assert control["hidden"]
    assert control.at_css("[data-gateway-update-button]")
    assert control.at_css("[data-gateway-update-message]")
  end
end

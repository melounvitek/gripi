require "minitest/autorun"
require "tmpdir"
require "json"
require "fileutils"
require "time"
require_relative "../lib/workspace_access_store"

class WorkspaceAccessStoreTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    @path = File.join(@root, "workspace-access.json")
  end

  def teardown
    FileUtils.remove_entry(@root) if @root && Dir.exist?(@root)
  end

  def test_rejects_new_pending_requests_when_the_store_is_full_but_returns_existing_request
    store = WorkspaceAccessStore.new(path: @path)
    WorkspaceAccessStore::MAX_PENDING_REQUESTS.times do |index|
      store.request_access("workspace-#{index}", browser_token: "browser")
    end

    existing = store.request_access("workspace-0", browser_token: "browser")
    error = assert_raises(WorkspaceAccessStore::PendingRequestsFull) do
      store.request_access("overflow", browser_token: "browser")
    end

    assert_equal "workspace-0", existing.fetch("workspace_id")
    assert_equal WorkspaceAccessStore::MAX_PENDING_REQUESTS, error.limit
    assert_equal WorkspaceAccessStore::MAX_PENDING_REQUESTS, read_state.fetch("pending_requests").length
  end

  def test_approval_and_denial_restore_capacity_for_new_active_requests
    store = WorkspaceAccessStore.new(path: @path)
    WorkspaceAccessStore::MAX_PENDING_REQUESTS.times do |index|
      store.request_access("workspace-#{index}", browser_token: "browser")
    end
    codes = read_state.fetch("pending_requests").first(2).map { |request| request.fetch("code") }

    store.approve_code(codes.first)
    store.deny_code(codes.last)
    store.request_access("replacement-1", browser_token: "browser")
    store.request_access("replacement-2", browser_token: "browser")

    active_count = read_state.fetch("pending_requests").count do |request|
      !request["approved_at"] && !request["denied_at"]
    end
    assert_equal WorkspaceAccessStore::MAX_PENDING_REQUESTS, active_count
  end

  def test_prunes_expired_requests_before_enforcing_the_cap
    expired_at = (Time.now.utc - WorkspaceAccessStore::PENDING_RETENTION - 1).iso8601
    write_state(
      "approved_workspaces" => [],
      "pending_requests" => WorkspaceAccessStore::MAX_PENDING_REQUESTS.times.map do |index|
        {
          "code" => "OLD#{index}",
          "workspace_id" => "old-#{index}",
          "browser_token" => "browser",
          "created_at" => expired_at,
          "requested_at" => expired_at
        }
      end
    )

    request = WorkspaceAccessStore.new(path: @path).request_access("new", browser_token: "browser")

    assert_equal "new", request.fetch("workspace_id")
    assert_equal ["new"], read_state.fetch("pending_requests").map { |item| item.fetch("workspace_id") }
  end

  def test_prunes_expired_pending_denied_and_approved_requests_on_read
    expired_at = (Time.now.utc - WorkspaceAccessStore::PENDING_RETENTION - 1).iso8601
    recent_at = Time.now.utc.iso8601
    write_state(
      "approved_workspaces" => [{ "workspace_id" => "approved", "approved_at" => expired_at }],
      "pending_requests" => [
        request("expired-pending", expired_at),
        request("expired-denied", expired_at).merge("denied_at" => expired_at),
        request("expired-approved", expired_at).merge("approved_at" => expired_at),
        request("recent", recent_at)
      ]
    )

    requests = WorkspaceAccessStore.new(path: @path).pending_requests

    assert_equal ["recent"], requests.map { |item| item.fetch("workspace_id") }
    assert_equal ["recent"], read_state.fetch("pending_requests").map { |item| item.fetch("workspace_id") }
    assert_equal ["approved"], read_state.fetch("approved_workspaces").map { |item| item.fetch("workspace_id") }
  end

  def test_expired_request_is_unknown_and_cannot_be_approved
    expired_at = (Time.now.utc - WorkspaceAccessStore::PENDING_RETENTION - 1).iso8601
    write_state(
      "approved_workspaces" => [],
      "pending_requests" => [request("expired", expired_at)]
    )
    store = WorkspaceAccessStore.new(path: @path)

    assert_nil store.approve_code("EXPIRED")
    assert_equal "unknown", store.pending_status("expired")
    assert_empty read_state.fetch("pending_requests")
  end

  private

  def request(workspace_id, timestamp)
    {
      "code" => workspace_id == "expired" ? "EXPIRED" : workspace_id.upcase,
      "workspace_id" => workspace_id,
      "browser_token" => "browser",
      "created_at" => timestamp,
      "requested_at" => timestamp
    }
  end

  def write_state(state)
    File.write(@path, JSON.pretty_generate(state) + "\n")
  end

  def read_state
    JSON.parse(File.read(@path))
  end
end

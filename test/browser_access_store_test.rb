require "minitest/autorun"
require "tmpdir"
require "json"
require "fileutils"
require_relative "../lib/browser_access_store"

class BrowserAccessStoreTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    @path = File.join(@root, "browser-access.json")
  end

  def teardown
    FileUtils.remove_entry(@root) if @root && Dir.exist?(@root)
  end

  def test_rejects_new_pending_requests_when_the_store_is_full_but_allows_existing_requests
    store = BrowserAccessStore.new(path: @path)
    BrowserAccessStore::MAX_PENDING_REQUESTS.times do |index|
      store.ensure_pending(token: "token-#{index}", ip: "127.0.0.1", user_agent: "test")
    end

    existing = store.request_access("token-0")
    error = assert_raises(BrowserAccessStore::PendingRequestsFull) do
      store.ensure_pending(token: "overflow", ip: "127.0.0.1", user_agent: "test")
    end

    assert existing.fetch("requested")
    assert_equal BrowserAccessStore::MAX_PENDING_REQUESTS, error.limit
    assert_equal BrowserAccessStore::MAX_PENDING_REQUESTS, read_state.fetch("pending_requests").length
  end

  def test_denial_restores_capacity_for_a_new_active_request
    store = BrowserAccessStore.new(path: @path)
    BrowserAccessStore::MAX_PENDING_REQUESTS.times do |index|
      store.ensure_pending(token: "token-#{index}", ip: "", user_agent: "")
    end
    denied_code = read_state.fetch("pending_requests").first.fetch("code")

    store.deny_code(denied_code)
    created = store.ensure_pending(token: "replacement", ip: "", user_agent: "")

    assert_equal "replacement", created.fetch("token")
    active_count = read_state.fetch("pending_requests").count { |request| !request["denied_at"] }
    assert_equal BrowserAccessStore::MAX_PENDING_REQUESTS, active_count
  end

  def test_bounds_persisted_ip_and_user_agent
    BrowserAccessStore.new(path: @path).ensure_pending(
      token: "browser",
      ip: "i" * (BrowserAccessStore::MAX_IP_BYTES + 10),
      user_agent: "a" + ("ü" * BrowserAccessStore::MAX_USER_AGENT_BYTES)
    )

    request = read_state.fetch("pending_requests").first
    assert_operator request.fetch("ip").bytesize, :<=, BrowserAccessStore::MAX_IP_BYTES
    assert_operator request.fetch("user_agent").bytesize, :<=, BrowserAccessStore::MAX_USER_AGENT_BYTES
    assert request.fetch("user_agent").valid_encoding?
  end

  def test_prunes_stale_pending_requests_before_enforcing_the_cap
    old_time = (Time.now.utc - (31 * 24 * 60 * 60)).iso8601
    write_state(
      "approved_browsers" => [],
      "pending_requests" => BrowserAccessStore::MAX_PENDING_REQUESTS.times.map do |index|
        { "code" => "OLD#{index}", "token" => "old-#{index}", "requested" => true, "created_at" => old_time, "requested_at" => old_time }
      end
    )

    request = BrowserAccessStore.new(path: @path).ensure_pending(token: "new", ip: "", user_agent: "")

    assert_equal "new", request.fetch("token")
    assert_equal ["new"], read_state.fetch("pending_requests").map { |item| item.fetch("token") }
  end

  def test_prunes_pending_request_with_malformed_timestamp
    write_state(
      "approved_browsers" => [],
      "pending_requests" => [
        { "code" => "BAD1", "token" => "bad", "requested" => true, "created_at" => "invalid", "requested_at" => "invalid" }
      ]
    )

    BrowserAccessStore.new(path: @path).ensure_pending(token: "new", ip: "", user_agent: "")

    assert_equal ["new"], read_state.fetch("pending_requests").map { |request| request.fetch("token") }
  end

  def test_prunes_stale_pending_requests_when_writing
    old_time = (Time.now.utc - (31 * 24 * 60 * 60)).iso8601
    recent_time = Time.now.utc.iso8601
    write_state(
      "approved_browsers" => [{ "token" => "approved", "approved_at" => old_time, "label" => "keep" }],
      "pending_requests" => [
        { "code" => "OLD1", "token" => "old-unrequested", "requested" => false, "created_at" => old_time, "requested_at" => nil },
        { "code" => "OLD2", "token" => "old-denied", "requested" => true, "created_at" => old_time, "requested_at" => old_time, "denied_at" => old_time },
        { "code" => "OLD3", "token" => "old-requested", "requested" => true, "created_at" => old_time, "requested_at" => old_time },
        { "code" => "NEW1", "token" => "recent", "requested" => true, "created_at" => recent_time, "requested_at" => recent_time }
      ]
    )

    BrowserAccessStore.new(path: @path).ensure_pending(token: "new", ip: "127.0.0.1", user_agent: "test")

    state = read_state
    assert_equal ["approved"], state.fetch("approved_browsers").map { |browser| browser.fetch("token") }
    assert_equal ["recent", "new"], state.fetch("pending_requests").map { |request| request.fetch("token") }
  end

  def test_prunes_stale_pending_requests_from_approved_check_at_most_once_per_day
    old_time = (Time.now.utc - (8 * 24 * 60 * 60)).iso8601
    write_state(
      "approved_browsers" => [{ "token" => "approved", "approved_at" => old_time, "label" => "keep" }],
      "pending_requests" => [
        { "code" => "OLD1", "token" => "old-unrequested", "requested" => false, "created_at" => old_time, "requested_at" => nil }
      ]
    )
    store = BrowserAccessStore.new(path: @path)

    assert store.approved?("approved")

    state = read_state
    assert_equal ["approved"], state.fetch("approved_browsers").map { |browser| browser.fetch("token") }
    assert_empty state.fetch("pending_requests")
  end

  def test_stale_pending_request_cannot_be_approved
    old_time = (Time.now.utc - (31 * 24 * 60 * 60)).iso8601
    write_state(
      "approved_browsers" => [],
      "pending_requests" => [
        { "code" => "OLD1", "token" => "old-requested", "requested" => true, "created_at" => old_time, "requested_at" => old_time }
      ]
    )

    result = BrowserAccessStore.new(path: @path).approve_code("OLD1")

    assert_nil result
    state = read_state
    assert_empty state.fetch("approved_browsers")
    assert_empty state.fetch("pending_requests")
  end

  def test_stale_pending_status_returns_created_after_pruning
    old_time = (Time.now.utc - (31 * 24 * 60 * 60)).iso8601
    write_state(
      "approved_browsers" => [],
      "pending_requests" => [
        { "code" => "OLD1", "token" => "old-requested", "requested" => true, "created_at" => old_time, "requested_at" => old_time }
      ]
    )

    status = BrowserAccessStore.new(path: @path).pending_status("old-requested")

    assert_equal "created", status
    assert_empty read_state.fetch("pending_requests")
  end

  private

  def write_state(state)
    File.write(@path, JSON.pretty_generate(state) + "\n")
  end

  def read_state
    JSON.parse(File.read(@path))
  end
end

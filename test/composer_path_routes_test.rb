ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "tmpdir"
require "fileutils"
require "json"
require "openssl"
require_relative "../app"

class ComposerPathRoutesTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    @sessions_root = File.join(@root, "sessions")
    @project = File.join(@root, "project")
    @other_project = File.join(@root, "other-project")
    FileUtils.mkdir_p([@sessions_root, @project, @other_project])
    Gripi.set :sessions_root, @sessions_root
    Gripi.set :browser_auth_disabled, true
    Gripi.set :multi_user_mode, false
    Gripi.set :workspace_secret_path, File.join(@root, "workspace-secret")
    Gripi.set :workspace_access_path, File.join(@root, "workspace-access.json")
    Gripi.set :workspace_ownership_path, File.join(@root, "session-owners.json")
    Gripi.set :rpc_idle_timeout_seconds, 0
    Gripi.set :pending_session_registry, Rpc::PendingSessionRegistry.new
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    Gripi.set :multi_user_mode, false
    FileUtils.remove_entry(@root) if Dir.exist?(@root)
  end

  def test_returns_structured_path_suggestions_from_the_sessions_authoritative_cwd
    session_path = write_session(@project)
    FileUtils.mkdir_p(File.join(@project, "docs"))
    File.write(File.join(@project, "draft.txt"), "")

    response = post_suggestions(session_path, "path", "d")

    assert_equal 200, response.status
    assert_equal "no-store", response["Cache-Control"]
    assert_equal [
      { "path" => "docs/", "directory" => true },
      { "path" => "draft.txt", "directory" => false }
    ], JSON.parse(response.body).fetch("suggestions")
  end

  def test_supports_a_known_pending_session_using_pending_metadata_cwd
    pending_path = File.join(@sessions_root, "pending-session.jsonl")
    Gripi.settings.pending_session_registry.remember(pending_path, @project)
    File.write(File.join(@project, "pending-match.txt"), "")

    response = post_suggestions(pending_path, "path", "pending")

    assert_equal 200, response.status
    assert_equal "pending-match.txt", JSON.parse(response.body).fetch("suggestions").first.fetch("path")
  end

  def test_rejects_unknown_noncanonical_and_other_workspace_sessions
    own_path = write_session(@project, "own.jsonl")
    other_path = write_session(@other_project, "other.jsonl")
    cookie, workspace_id = approved_workspace_cookie("workspace-user")
    ownership = WorkspaceSessionOwnershipStore.new(path: Gripi.settings.workspace_ownership_path)
    ownership.claim(own_path, workspace_id)
    ownership.claim(other_path, "another-workspace")
    Gripi.set :multi_user_mode, true

    unknown = post_suggestions(File.join(@sessions_root, "unknown.jsonl"), "path", "", "HTTP_COOKIE" => cookie)
    noncanonical = post_suggestions(File.join(File.dirname(own_path), ".", File.basename(own_path)), "path", "", "HTTP_COOKIE" => cookie)
    other = post_suggestions(other_path, "path", "", "HTTP_COOKIE" => cookie)

    assert_equal 404, unknown.status
    assert_equal 404, noncanonical.status
    assert_equal 404, other.status
  end

  def test_rejects_sessions_without_an_absolute_cwd_or_outside_the_sessions_root
    missing_cwd = File.join(@sessions_root, "missing-cwd.jsonl")
    relative_cwd = File.join(@sessions_root, "relative-cwd.jsonl")
    outside_session = File.join(@root, "outside.jsonl")
    linked_session = File.join(@sessions_root, "linked.jsonl")
    File.write(missing_cwd, JSON.generate(type: "session", id: "missing") + "\n")
    File.write(relative_cwd, JSON.generate(type: "session", id: "relative", cwd: "project") + "\n")
    File.write(outside_session, JSON.generate(type: "session", id: "outside", cwd: @project) + "\n")
    File.symlink(outside_session, linked_session)

    assert_equal 404, post_suggestions(missing_cwd, "path", "").status
    assert_equal 404, post_suggestions(relative_cwd, "path", "").status
    assert_equal 404, post_suggestions(linked_session, "path", "").status
  end

  def test_rejects_invalid_mode_oversized_or_malformed_query_and_cross_origin_requests
    session_path = write_session(@project)

    invalid_mode = post_suggestions(session_path, "glob", "item")
    oversized = post_suggestions(session_path, "path", "x" * 1_025)
    malformed = post_suggestions(session_path, "path", "bad\0path")
    cross_origin = post_suggestions(session_path, "path", "", "HTTP_ORIGIN" => "http://evil.example")

    assert_equal 400, invalid_mode.status
    assert_equal 400, oversized.status
    assert_equal 400, malformed.status
    assert_equal 403, cross_origin.status
  end

  private

  def post_suggestions(session, mode, query, headers = {})
    @request.post(
      "/composer/path_suggestions",
      { params: { "session" => session, "mode" => mode, "query" => query } }.merge(headers)
    )
  end

  def write_session(cwd, name = "session.jsonl")
    directory = File.join(@sessions_root, cwd == @project ? "project" : "other")
    FileUtils.mkdir_p(directory)
    path = File.join(directory, name)
    File.write(path, JSON.generate(type: "session", id: name, cwd: cwd) + "\n")
    path
  end

  def approved_workspace_cookie(key)
    secret = WorkspaceSecretStore.new(path: Gripi.settings.workspace_secret_path).secret
    workspace_id = OpenSSL::HMAC.hexdigest("SHA256", secret, key)
    WorkspaceAccessStore.new(path: Gripi.settings.workspace_access_path).approve_workspace(workspace_id)
    ["gripi_workspace=#{workspace_id}", workspace_id]
  end
end

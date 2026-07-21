require "minitest/autorun"
require "tmpdir"
require "json"
require "fileutils"
require_relative "../lib/browser_access_store"
require_relative "../lib/workspace_access_store"
require_relative "../lib/workspace_secret_store"
require_relative "../lib/workspace_session_ownership_store"

class LocalCredentialStateTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
  end

  def teardown
    FileUtils.remove_entry(@root) if @root && Dir.exist?(@root)
  end

  def test_credential_files_are_created_with_owner_only_permissions
    paths = credential_paths

    BrowserAccessStore.new(path: paths.fetch(:browser)).ensure_pending(token: "browser", ip: "127.0.0.1", user_agent: "test")
    WorkspaceAccessStore.new(path: paths.fetch(:workspace)).approve_workspace("workspace")
    WorkspaceSessionOwnershipStore.new(path: paths.fetch(:owners)).claim("/tmp/session.jsonl", "workspace")
    WorkspaceSecretStore.new(path: paths.fetch(:secret)).secret

    paths.each_value { |path| assert_equal 0o600, File.stat(path).mode & 0o777, path }
  end

  def test_loading_existing_credential_files_repairs_permissions
    paths = credential_paths
    FileUtils.mkdir_p(File.dirname(paths.fetch(:browser)))
    File.write(paths.fetch(:browser), JSON.generate("approved_browsers" => [], "pending_requests" => []))
    File.write(paths.fetch(:workspace), JSON.generate("approved_workspaces" => [], "pending_requests" => []))
    File.write(paths.fetch(:owners), JSON.generate("sessions" => {}))
    File.write(paths.fetch(:secret), "existing-secret\n")
    paths.each_value { |path| File.chmod(0o644, path) }

    BrowserAccessStore.new(path: paths.fetch(:browser)).pending_requests
    WorkspaceAccessStore.new(path: paths.fetch(:workspace)).pending_requests
    WorkspaceSessionOwnershipStore.new(path: paths.fetch(:owners)).owned_by?("/tmp/session.jsonl", "workspace")
    assert_equal "existing-secret", WorkspaceSecretStore.new(path: paths.fetch(:secret)).secret

    paths.each_value { |path| assert_equal 0o600, File.stat(path).mode & 0o777, path }
  end

  def test_concurrent_workspace_secret_creation_returns_one_persisted_secret
    path = credential_paths.fetch(:secret)
    ready = Queue.new
    start = Queue.new
    threads = 20.times.map do
      Thread.new do
        ready << true
        start.pop
        WorkspaceSecretStore.new(path: path).secret
      end
    end
    20.times { ready.pop }
    20.times { start << true }

    secrets = threads.map(&:value)

    assert_equal 1, secrets.uniq.length
    assert_equal File.read(path).strip, secrets.first
  end

  def test_default_state_directory_is_owner_only_but_custom_parent_is_not_changed
    default_directory = File.join(@root, ".pi", "gripi")
    custom_directory = File.join(@root, "custom")
    FileUtils.mkdir_p(default_directory, mode: 0o755)
    FileUtils.mkdir_p(custom_directory, mode: 0o755)

    original_home = ENV["HOME"]
    ENV["HOME"] = @root
    BrowserAccessStore.new(path: File.join(default_directory, "browser-access.json")).ensure_pending(token: "default", ip: "", user_agent: "")
    BrowserAccessStore.new(path: File.join(custom_directory, "browser-access.json")).ensure_pending(token: "custom", ip: "", user_agent: "")

    assert_equal 0o700, File.stat(default_directory).mode & 0o777
    assert_equal 0o755, File.stat(custom_directory).mode & 0o777
  ensure
    ENV["HOME"] = original_home
  end

  def test_atomic_writes_do_not_use_the_predictable_dot_tmp_path
    path = credential_paths.fetch(:browser)
    FileUtils.mkdir_p(File.dirname(path))
    target = File.join(@root, "symlink-target")
    File.write(target, "untouched")
    File.symlink(target, "#{path}.tmp")

    BrowserAccessStore.new(path: path).ensure_pending(token: "browser", ip: "", user_agent: "")

    assert_equal "untouched", File.read(target)
    assert File.symlink?("#{path}.tmp")
  end

  private

  def credential_paths
    directory = File.join(@root, "state")
    {
      browser: File.join(directory, "browser-access.json"),
      workspace: File.join(directory, "workspace-access.json"),
      owners: File.join(directory, "session-owners.json"),
      secret: File.join(directory, "workspace-secret")
    }
  end
end

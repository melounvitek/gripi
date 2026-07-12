require "fileutils"
require "minitest/autorun"
require "open3"
require "tmpdir"
require_relative "../lib/gateway_updater"

class GatewayUpdaterTest < Minitest::Test
  def setup
    @tmpdir = Dir.mktmpdir
    @origin = File.join(@tmpdir, "origin.git")
    @upstream = File.join(@tmpdir, "upstream")
    @checkout = File.join(@tmpdir, "gateway;touch injected")

    git("init", "--bare", "--initial-branch=master", @origin, chdir: @tmpdir)
    git("init", "--initial-branch=master", @upstream, chdir: @tmpdir)
    git("config", "user.email", "gateway@example.test", chdir: @upstream)
    git("config", "user.name", "Gateway Test", chdir: @upstream)
    File.write(File.join(@upstream, "app.txt"), "initial\n")
    git("add", "app.txt", chdir: @upstream)
    git("commit", "-m", "Initial version", chdir: @upstream)
    git("remote", "add", "origin", @origin, chdir: @upstream)
    git("push", "-u", "origin", "master", chdir: @upstream)
    git("clone", @origin, @checkout, chdir: @tmpdir)
    git("config", "user.email", "gateway@example.test", chdir: @checkout)
    git("config", "user.name", "Gateway Test", chdir: @checkout)
  end

  def teardown
    FileUtils.remove_entry(@tmpdir) if @tmpdir
  end

  def test_reports_up_to_date_with_current_and_target_revisions
    status = updater.status

    assert_equal :up_to_date, status.state
    assert_nil status.reason
    assert_match(/\A[0-9a-f]{7,}\z/, status.current_sha)
    assert_equal status.current_sha, status.target_sha
    assert_equal 0, status.ahead_count
    assert_equal 0, status.behind_count
  end

  def test_reports_an_available_fast_forward_with_commit_details
    upstream_commit("first.txt", "first\n", "Add first update")
    target_sha = upstream_commit("second.txt", "second\n", "Add second update")

    status = updater.status

    assert_equal :available, status.state
    assert_nil status.reason
    assert_equal target_sha[0, status.target_sha.length], status.target_sha
    assert_equal target_sha, status.target_revision
    assert_equal 0, status.ahead_count
    assert_equal 2, status.behind_count
    assert_includes status.summary, "Add first update"
    assert_includes status.summary, "Add second update"
  end

  def test_blocks_a_non_master_branch
    git("switch", "-c", "feature", chdir: @checkout)

    status = updater.status

    assert_equal :blocked, status.state
    assert_equal :branch, status.reason
    assert_match(/master/, status.message)
  end

  def test_blocks_tracked_changes
    File.write(File.join(@checkout, "app.txt"), "changed\n")

    status = updater.status

    assert_equal :blocked, status.state
    assert_equal :dirty, status.reason
  end

  def test_blocks_untracked_files
    File.write(File.join(@checkout, "untracked.txt"), "changed\n")

    status = updater.status

    assert_equal :blocked, status.state
    assert_equal :dirty, status.reason
  end

  def test_blocks_local_commits
    local_commit("local.txt", "local\n", "Local change")

    status = updater.status

    assert_equal :blocked, status.state
    assert_equal :ahead, status.reason
    assert_equal 1, status.ahead_count
    assert_equal 0, status.behind_count
    assert_includes status.summary, "Local change"
  end

  def test_blocks_diverged_history
    local_commit("local.txt", "local\n", "Local change")
    upstream_commit("remote.txt", "remote\n", "Remote change")

    status = updater.status

    assert_equal :blocked, status.state
    assert_equal :diverged, status.reason
    assert_equal 1, status.ahead_count
    assert_equal 1, status.behind_count
  end

  def test_reports_fetch_failures_as_operational_errors
    git("remote", "set-url", "origin", File.join(@tmpdir, "missing.git"), chdir: @checkout)

    status = updater.status

    assert_equal :error, status.state
    assert_equal :fetch, status.reason
    assert_match(/fetch/i, status.message)
  end

  def test_fast_forwards_and_runs_the_dependency_installer
    target_sha = upstream_commit("new.txt", "new\n", "Add update")
    installer_calls = []
    dependency_installer = ->(directory) { installer_calls << directory; true }

    result = updater(dependency_installer: dependency_installer).update

    assert_equal :updated, result.state
    assert_equal target_sha, git("rev-parse", "HEAD", chdir: @checkout).strip
    assert_equal [@checkout], installer_calls
    refute File.exist?(File.join(@tmpdir, "injected"))
  end

  def test_default_dependency_installer_runs_bundle_install_without_a_shell_and_reports_failures
    old_sha = git("rev-parse", "HEAD", chdir: @checkout).strip
    upstream_commit("new.txt", "new\n", "Add update")
    bin_dir = File.join(@tmpdir, "bin")
    record_path = File.join(@tmpdir, "bundle-call")
    FileUtils.mkdir_p(bin_dir)
    File.write(File.join(bin_dir, "bundle"), <<~SH)
      #!/bin/sh
      printf '%s\n' "$PWD" "$#" "$1" > "$BUNDLE_CALL_RECORD"
      printf 'Could not resolve dependencies\n' >&2
      exit 1
    SH
    FileUtils.chmod(0o755, File.join(bin_dir, "bundle"))
    old_path = ENV["PATH"]
    old_record = ENV["BUNDLE_CALL_RECORD"]
    ENV["PATH"] = "#{bin_dir}:#{old_path}"
    ENV["BUNDLE_CALL_RECORD"] = record_path

    result = updater.update

    assert_equal :dependency_failed, result.state
    assert_equal [@checkout, "1", "install"], File.readlines(record_path, chomp: true)
    assert_includes result.message, "Could not resolve dependencies"
    assert_equal old_sha, git("rev-parse", "HEAD", chdir: @checkout).strip
    refute File.exist?(File.join(@tmpdir, "injected"))
  ensure
    ENV["PATH"] = old_path
    ENV["BUNDLE_CALL_RECORD"] = old_record
  end

  def test_update_rechecks_preconditions
    upstream_commit("new.txt", "new\n", "Add update")
    assert_equal :available, updater.status.state
    File.write(File.join(@checkout, "untracked.txt"), "changed\n")
    installer_called = false

    result = updater(dependency_installer: ->(_directory) { installer_called = true }).update

    assert_equal :blocked, result.state
    assert_equal :dirty, result.status.reason
    refute installer_called
    refute File.exist?(File.join(@checkout, "new.txt"))
  end

  def test_rolls_back_to_the_old_revision_when_dependency_installation_fails
    old_sha = git("rev-parse", "HEAD", chdir: @checkout).strip
    upstream_commit("app.txt", "updated\n", "Update dependencies")

    result = updater(dependency_installer: ->(_directory) { raise "Bundler failed" }).update

    assert_equal :dependency_failed, result.state
    assert result.rolled_back
    assert_includes result.message, "Bundler failed"
    assert_equal old_sha, git("rev-parse", "HEAD", chdir: @checkout).strip
    assert_equal "initial\n", File.read(File.join(@checkout, "app.txt"))
    assert_empty git("status", "--porcelain", chdir: @checkout)
  end

  private

  def updater(dependency_installer: nil)
    GatewayUpdater.new(@checkout, dependency_installer: dependency_installer)
  end

  def upstream_commit(path, content, message)
    File.write(File.join(@upstream, path), content)
    git("add", path, chdir: @upstream)
    git("commit", "-m", message, chdir: @upstream)
    git("push", "origin", "master", chdir: @upstream)
    git("rev-parse", "HEAD", chdir: @upstream).strip
  end

  def local_commit(path, content, message)
    File.write(File.join(@checkout, path), content)
    git("add", path, chdir: @checkout)
    git("commit", "-m", message, chdir: @checkout)
  end

  def git(*arguments, chdir:)
    stdout, stderr, status = Open3.capture3("git", *arguments, chdir: chdir)
    raise "git #{arguments.join(" ")} failed: #{stderr}" unless status.success?

    stdout
  end
end

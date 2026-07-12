require "fileutils"
require "minitest/autorun"
require "open3"
require "tmpdir"

class StartTest < Minitest::Test
  def setup
    @tmpdir = Dir.mktmpdir
    @bin_dir = File.join(@tmpdir, "bin")
    @launcher = File.join(@tmpdir, "start")
    @calls_path = File.join(@tmpdir, "bundle-calls")
    @restart_path = File.join(@tmpdir, "state", "restart-request")
    FileUtils.mkdir_p(@bin_dir)
    FileUtils.cp(File.expand_path("../bin/start", __dir__), @launcher)
    FileUtils.chmod(0o755, @launcher)
  end

  def teardown
    FileUtils.remove_entry(@tmpdir) if @tmpdir
  end

  def test_ordinary_exit_preserves_status_without_restarting
    write_fake_bundle(<<~SH)
      printf '%s\n' "$*|$RACK_ENV" >> "$CALLS_PATH"
      exit 23
    SH

    _stdout, _stderr, status = run_launcher("127.0.0.1", "PI_GATEWAY_PORT" => "5678")

    assert_equal 23, status.exitstatus
    assert_equal ["exec rackup -o 127.0.0.1 -p 5678|production"], File.readlines(@calls_path, chomp: true)
    refute File.exist?(@restart_path)
  end

  def test_stale_restart_marker_is_cleared_before_launch
    FileUtils.mkdir_p(File.dirname(@restart_path))
    FileUtils.touch(@restart_path)
    write_fake_bundle(<<~SH)
      printf 'run\n' >> "$CALLS_PATH"
      exit 31
    SH

    _stdout, _stderr, status = run_launcher

    assert_equal 31, status.exitstatus
    assert_equal ["run"], File.readlines(@calls_path, chomp: true)
    refute File.exist?(@restart_path)
  end

  def test_restart_marker_is_consumed_and_causes_exactly_one_relaunch
    write_fake_bundle(<<~SH)
      count=$(wc -l < "$CALLS_PATH" 2>/dev/null || printf 0)
      printf 'run\n' >> "$CALLS_PATH"
      if [ "$count" -eq 0 ]; then
        mkdir -p "$(dirname "$RESTART_PATH")"
        touch "$RESTART_PATH"
        exit 17
      fi
      exit 29
    SH

    _stdout, _stderr, status = run_launcher

    assert_equal 29, status.exitstatus
    assert_equal ["run", "run"], File.readlines(@calls_path, chomp: true)
    refute File.exist?(@restart_path)
  end

  def test_relaunch_reads_updated_launcher_and_does_not_use_systemctl
    update_log = File.join(@tmpdir, "updated-launcher")
    updated_launcher = File.join(@tmpdir, "updated-start")
    launcher_source = File.read(@launcher).sub("#!/usr/bin/env bash\n", <<~SH)
      #!/usr/bin/env bash
      printf 'updated\n' >> "$UPDATE_LOG"
    SH
    File.write(updated_launcher, launcher_source)
    FileUtils.chmod(0o755, updated_launcher)
    systemctl_log = File.join(@tmpdir, "systemctl-calls")
    write_executable("systemctl", <<~SH)
      printf 'called\n' >> "$SYSTEMCTL_LOG"
      exit 99
    SH
    write_fake_bundle(<<~SH)
      count=$(wc -l < "$CALLS_PATH" 2>/dev/null || printf 0)
      printf 'run\n' >> "$CALLS_PATH"
      if [ "$count" -eq 0 ]; then
        mv "$UPDATED_LAUNCHER" "$LAUNCHER"
        mkdir -p "$(dirname "$RESTART_PATH")"
        touch "$RESTART_PATH"
      fi
      exit 0
    SH

    _stdout, _stderr, status = run_launcher(nil,
      "LAUNCHER" => @launcher,
      "UPDATED_LAUNCHER" => updated_launcher,
      "UPDATE_LOG" => update_log,
      "SYSTEMCTL_LOG" => systemctl_log)

    assert status.success?, _stderr
    assert_equal ["run", "run"], File.readlines(@calls_path, chomp: true)
    assert_equal ["updated"], File.readlines(update_log, chomp: true)
    refute File.exist?(systemctl_log)
  end

  def test_default_restart_path_requires_home
    write_fake_bundle("exit 0\n")
    env = base_env.reject { |key, _value| key == "PI_GATEWAY_RESTART_PATH" }
    env["HOME"] = ""

    _stdout, stderr, status = Open3.capture3(env, @launcher)

    refute status.success?
    assert_match(/HOME|PI_GATEWAY_RESTART_PATH/, stderr)
    refute File.exist?(@calls_path)
  end

  private

  def run_launcher(host = nil, extra_env = {})
    command = [@launcher]
    command << host if host
    Open3.capture3(base_env.merge(extra_env), *command)
  end

  def base_env
    {
      "PATH" => "#{@bin_dir}:#{ENV.fetch("PATH")}",
      "CALLS_PATH" => @calls_path,
      "RESTART_PATH" => @restart_path,
      "PI_GATEWAY_RESTART_PATH" => @restart_path
    }
  end

  def write_fake_bundle(body)
    write_executable("bundle", body)
  end

  def write_executable(name, body)
    path = File.join(@bin_dir, name)
    File.write(path, "#!/bin/sh\n#{body}")
    FileUtils.chmod(0o755, path)
  end
end

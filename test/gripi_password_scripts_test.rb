require "minitest/autorun"
require "open3"
require "tmpdir"

class GripiPasswordScriptsTest < Minitest::Test
  def test_password_helper_generates_password_once_and_prints_change_warning
    Dir.mktmpdir do |dir|
      env_path = File.join(dir, "gateway-env")
      env = { "GRIPI_ENV_PATH" => env_path }

      first_stdout, first_stderr, first_status = Open3.capture3(env, "bin/gripi-password", chdir: repo_root)

      assert first_status.success?, first_stderr
      password = File.read(env_path).match(/\AGRIPI_ADMIN_PASSWORD=([0-9a-f]{24})\n\z/)[1]
      assert_includes first_stdout, "Generated GRIPI_ADMIN_PASSWORD in #{env_path}"
      assert_includes first_stdout, "Admin password: #{password}"
      assert_includes first_stdout, "You should change it by editing #{env_path}"

      second_stdout, second_stderr, second_status = Open3.capture3(env, "bin/gripi-password", chdir: repo_root)

      assert second_status.success?, second_stderr
      assert_empty second_stdout
      refute_includes second_stdout, password
    end
  end

  def test_setup_invokes_gripi_password_helper
    Dir.mktmpdir do |dir|
      bin_dir = File.join(dir, "bin")
      env_path = File.join(dir, "gripi-env")
      Dir.mkdir(bin_dir)
      File.write(File.join(bin_dir, "bundle"), "#!/bin/sh\nexit 0\n")
      File.chmod(0o755, File.join(bin_dir, "bundle"))

      env = {
        "PATH" => "#{bin_dir}:#{ENV.fetch("PATH")}",
        "GRIPI_ENV_PATH" => env_path
      }
      _stdout, stderr, status = Open3.capture3(env, "bin/setup", chdir: repo_root)

      assert status.success?, stderr
      assert_match(/\AGRIPI_ADMIN_PASSWORD=[0-9a-f]{24}\n\z/, File.read(env_path))
    end
  end

  private

  def repo_root
    File.expand_path("..", __dir__)
  end
end

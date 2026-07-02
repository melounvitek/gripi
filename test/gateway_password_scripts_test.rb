require "minitest/autorun"
require "open3"
require "tmpdir"

class GatewayPasswordScriptsTest < Minitest::Test
  def test_password_helper_generates_password_once_and_prints_change_warning
    Dir.mktmpdir do |dir|
      env_path = File.join(dir, "gateway-env")
      env = { "PI_GATEWAY_ENV_PATH" => env_path }

      first_stdout, first_stderr, first_status = Open3.capture3(env, "bin/gateway-password", chdir: repo_root)

      assert first_status.success?, first_stderr
      password = File.read(env_path).match(/\API_GATEWAY_ADMIN_PASSWORD=([0-9a-f]{24})\n\z/)[1]
      assert_includes first_stdout, "Generated PI_GATEWAY_ADMIN_PASSWORD in #{env_path}"
      assert_includes first_stdout, "Admin password: #{password}"
      assert_includes first_stdout, "You should change it by editing #{env_path}"

      second_stdout, second_stderr, second_status = Open3.capture3(env, "bin/gateway-password", chdir: repo_root)

      assert second_status.success?, second_stderr
      assert_empty second_stdout
      refute_includes second_stdout, password
    end
  end

  private

  def repo_root
    File.expand_path("..", __dir__)
  end
end

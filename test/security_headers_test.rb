ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "nokogiri"
require "tmpdir"
require "fileutils"
require_relative "../app"

class SecurityHeadersTest < Minitest::Test
  def setup
    @root = Dir.mktmpdir
    Gripi.set :browser_access_path, File.join(@root, "browser-access.json")
    Gripi.set :workspace_secret_path, File.join(@root, "workspace-secret")
    Gripi.set :workspace_access_path, File.join(@root, "workspace-access.json")
    Gripi.set :workspace_ownership_path, File.join(@root, "session-owners.json")
    Gripi.set :browser_auth_disabled, true
    Gripi.set :multi_user_mode, false
    Gripi.set :enforce_secure_remote_transport, false
    Gripi.set :trust_proxy_headers, false
    Gripi.set :access_request_rate_limiter, RequestRateLimiter.new(limit: 30, window: 60)
    Gripi.set :admin_login_rate_limiter, RequestRateLimiter.new(limit: 10, window: 300)
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    FileUtils.remove_entry(@root) if Dir.exist?(@root)
  end

  def test_html_uses_nonce_based_script_policy_and_baseline_headers
    response = @request.get("/notification-test")
    nonce = response["Content-Security-Policy"].match(/script-src 'self' 'nonce-([^']+)'/)[1]
    document = Nokogiri::HTML(response.body)

    assert_equal nonce, document.at_css("script")["nonce"]
    refute_includes response["Content-Security-Policy"], "script-src 'self' 'unsafe-inline'"
    assert_includes response["Content-Security-Policy"], "frame-ancestors 'none'"
    assert_equal "nosniff", response["X-Content-Type-Options"]
    assert_equal "no-referrer", response["Referrer-Policy"]
  end

  def test_https_responses_include_hsts
    response = @request.get("/notification-test", "rack.url_scheme" => "https")

    assert_equal "max-age=31536000", response["Strict-Transport-Security"]
  end

  def test_workspace_return_path_is_data_not_executable_script
    Gripi.set :browser_auth_disabled, false
    Gripi.set :gateway_admin_password, "secret"
    Gripi.set :multi_user_mode, true
    WorkspaceAccessStore.new(path: Gripi.settings.workspace_access_path).approve_workspace("existing")
    return_to = "/</script><script>window.injection=true</script>"

    response = @request.post(
      "/workspace-key",
      params: { "workspace_key" => "piu_different_horse_42", "return_to" => return_to }
    )
    document = Nokogiri::HTML(response.body)

    assert_equal 403, response.status
    assert_equal return_to, document.at_css("body")["data-workspace-return-to"]
    refute_includes response.body, "</script><script>window.injection=true"
    assert_includes document.at_css("script").text, "dataset.workspaceReturnTo"
  end
end

ENV["APP_ENV"] = "test"
ENV["GRIPI_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "tmpdir"
require "fileutils"
require_relative "../app"

class RequestTransportSecurityTest < Minitest::Test
  def setup
    @browser_root = Dir.mktmpdir
    Gripi.set :browser_access_path, File.join(@browser_root, "browser-access.json")
    Gripi.set :browser_auth_disabled, true
    Gripi.set :gateway_admin_password, "secret"
    Gripi.set :multi_user_mode, false
    Gripi.set :enforce_secure_remote_transport, true
    Gripi.set :trust_proxy_headers, false
    Gripi.set :rpc_idle_timeout_seconds, 0
    @request = Rack::MockRequest.new(Gripi)
  end

  def teardown
    Gripi.set :enforce_secure_remote_transport, false
    Gripi.set :trust_proxy_headers, false
    FileUtils.remove_entry(@browser_root) if Dir.exist?(@browser_root)
  end

  def test_allows_plain_http_from_loopback_client
    response = @request.get("/gateway-update", "REMOTE_ADDR" => "127.0.0.1")

    assert_equal 200, response.status
  end

  def test_rejects_plain_http_from_remote_client
    response = @request.get("/gateway-update", "REMOTE_ADDR" => "100.64.0.2")

    assert_equal 403, response.status
    assert_includes response.body, "HTTPS"
  end

  def test_allows_https_from_remote_client
    response = @request.get("/gateway-update", "REMOTE_ADDR" => "100.64.0.2", "rack.url_scheme" => "https")

    assert_equal 200, response.status
  end

  def test_rejects_trusted_forwarded_http_from_loopback_proxy
    Gripi.set :trust_proxy_headers, true

    response = @request.get(
      "/gateway-update",
      "REMOTE_ADDR" => "127.0.0.1",
      "HTTP_X_FORWARDED_PROTO" => "http"
    )

    assert_equal 403, response.status
  end

  def test_allows_trusted_forwarded_https_from_remote_client
    Gripi.set :trust_proxy_headers, true

    response = @request.get(
      "/gateway-update",
      "REMOTE_ADDR" => "10.0.0.2",
      "HTTP_X_FORWARDED_PROTO" => "https"
    )

    assert_equal 200, response.status
  end

  def test_browser_cookie_is_secure_over_https
    Gripi.set :browser_auth_disabled, false

    response = @request.get("/", "REMOTE_ADDR" => "100.64.0.2", "rack.url_scheme" => "https")

    assert_includes Array(response["Set-Cookie"]).join("\n"), "secure"
  end

  def test_browser_cookie_is_not_marked_secure_on_allowed_loopback_http
    Gripi.set :browser_auth_disabled, false

    response = @request.get("/", "REMOTE_ADDR" => "127.0.0.1")

    refute_includes Array(response["Set-Cookie"]).join("\n"), "secure"
  end

  def test_rejects_spoofed_forwarded_https_when_headers_are_untrusted
    response = @request.get(
      "/gateway-update",
      "REMOTE_ADDR" => "10.0.0.2",
      "HTTP_X_FORWARDED_PROTO" => "https"
    )

    assert_equal 403, response.status
  end
end

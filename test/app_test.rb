ENV["PI_GATEWAY_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "open3"
require "rbconfig"
require "tmpdir"
require "json"
require "fileutils"
require "base64"
require "pathname"
require "timeout"
require_relative "../app"

class AppTest < Minitest::Test
  APP_JAVASCRIPT = Dir[File.expand_path("../public/assets/*.js", __dir__)].sort.map { |path| File.read(path) }.join("\n")
  APP_STYLESHEET = File.read(File.expand_path("../public/assets/app.css", __dir__))

  def setup
    @attachments_root = Dir.mktmpdir
    @read_state_root = Dir.mktmpdir
    @browser_access_root = Dir.mktmpdir
    @workspace_root = Dir.mktmpdir
    PiWebGateway.set :attachments_root, @attachments_root
    PiWebGateway.set :read_state_path, File.join(@read_state_root, "read-state.json")
    PiWebGateway.set :browser_access_path, File.join(@browser_access_root, "browser-access.json")
    PiWebGateway.set :browser_auth_disabled, false
    PiWebGateway.set :multi_user_mode, false
    PiWebGateway.set :workspace_secret_path, File.join(@workspace_root, "workspace-secret")
    PiWebGateway.set :workspace_access_path, File.join(@workspace_root, "workspace-access.json")
    PiWebGateway.set :workspace_ownership_path, File.join(@workspace_root, "session-owners.json")
    PiWebGateway.set :gateway_admin_password, nil
    PiWebGateway.set :session_cwds_path, nil
    PiWebGateway.set :rpc_client_registry, nil
    PiWebGateway.set :session_synchronizer, nil
    PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new
    PiWebGateway.set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
    PiWebGateway.set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd) }]
  end

  def teardown
    FileUtils.remove_entry(@attachments_root) if @attachments_root && Dir.exist?(@attachments_root)
    FileUtils.remove_entry(@read_state_root) if @read_state_root && Dir.exist?(@read_state_root)
    FileUtils.remove_entry(@browser_access_root) if @browser_access_root && Dir.exist?(@browser_access_root)
    FileUtils.remove_entry(@workspace_root) if @workspace_root && Dir.exist?(@workspace_root)
  end

  def test_app_boot_fails_without_admin_password
    Dir.mktmpdir do |home|
      env = ENV.to_h.merge("PI_GATEWAY_ENV_PATH" => File.join(home, "missing-env"), "PI_GATEWAY_ADMIN_PASSWORD" => nil)

      _stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'")

      refute status.success?
      assert_includes stderr, "PI_GATEWAY_ADMIN_PASSWORD is required"
    end
  end

  def test_app_boot_allows_missing_admin_password_when_browser_auth_is_disabled_for_single_user
    Dir.mktmpdir do |home|
      env = ENV.to_h.merge(
        "PI_GATEWAY_ENV_PATH" => File.join(home, "missing-env"),
        "PI_GATEWAY_ADMIN_PASSWORD" => nil,
        "PI_BROWSER_AUTH_DISABLED" => "1",
        "PI_MULTI_USER_MODE" => nil
      )

      stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.browser_auth_disabled")

      assert status.success?, stderr
      assert_equal "true", stdout.strip
    end
  end

  def test_app_boot_allows_missing_admin_password_when_browser_auth_is_disabled_for_multi_user
    Dir.mktmpdir do |home|
      env = ENV.to_h.merge(
        "PI_GATEWAY_ENV_PATH" => File.join(home, "missing-env"),
        "PI_GATEWAY_ADMIN_PASSWORD" => nil,
        "PI_BROWSER_AUTH_DISABLED" => "1",
        "PI_MULTI_USER_MODE" => "1"
      )

      stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.multi_user_mode")

      assert status.success?, stderr
      assert_equal "true", stdout.strip
    end
  end

  def test_app_boot_loads_admin_password_from_user_config
    Dir.mktmpdir do |home|
      env_path = File.join(home, "gateway-env")
      File.write(env_path, "PI_GATEWAY_ADMIN_PASSWORD='from-file'\n")
      env = ENV.to_h.merge("PI_GATEWAY_ENV_PATH" => env_path, "PI_GATEWAY_ADMIN_PASSWORD" => nil)

      stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.gateway_admin_password")

      assert status.success?, stderr
      assert_equal "from-file", stdout.strip
    end
  end

  def test_app_boot_loads_session_cwds_path_from_user_config
    Dir.mktmpdir do |home|
      env_path = File.join(home, "gateway-env")
      session_cwds_path = File.join(home, "pinned-dirs")
      File.write(env_path, "PI_GATEWAY_ADMIN_PASSWORD='from-file'\nPI_SESSION_CWDS_PATH=#{session_cwds_path}\n")
      env = ENV.to_h.merge("PI_GATEWAY_ENV_PATH" => env_path, "PI_GATEWAY_ADMIN_PASSWORD" => nil, "PI_SESSION_CWDS_PATH" => nil)

      stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.session_cwds_path")

      assert status.success?, stderr
      assert_equal session_cwds_path, stdout.strip
    end
  end

  def test_app_boot_configures_pi_rpc_command_from_node_and_pi_paths
    env = ENV.to_h.merge(
      "PI_GATEWAY_ADMIN_PASSWORD" => "secret",
      "PI_GATEWAY_ENV_PATH" => File.join(Dir.tmpdir, "missing-pi-web-gateway-env"),
      "PI_GATEWAY_NODE" => "/opt/node",
      "PI_GATEWAY_PI" => "/opt/pi"
    )

    stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.pi_rpc_command_prefix.join(' ')")

    assert status.success?, stderr
    assert_equal "/opt/node /opt/pi", stdout.strip
  end

  def test_app_boot_rejects_partial_pi_rpc_command_config
    env = ENV.to_h.merge(
      "PI_GATEWAY_ADMIN_PASSWORD" => "secret",
      "PI_GATEWAY_ENV_PATH" => File.join(Dir.tmpdir, "missing-pi-web-gateway-env"),
      "PI_GATEWAY_NODE" => "/opt/node",
      "PI_GATEWAY_PI" => nil
    )

    _stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'")

    refute status.success?
    assert_includes stderr, "PI_GATEWAY_NODE and PI_GATEWAY_PI must be set together"
  end

  def test_app_boot_prefers_process_env_over_user_config
    Dir.mktmpdir do |home|
      env_path = File.join(home, "gateway-env")
      File.write(env_path, "PI_GATEWAY_ADMIN_PASSWORD='from-file'\n")
      env = ENV.to_h.merge("PI_GATEWAY_ENV_PATH" => env_path, "PI_GATEWAY_ADMIN_PASSWORD" => "from-process")

      stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", "require './app'; puts PiWebGateway.settings.gateway_admin_password")

      assert status.success?, stderr
      assert_equal "from-process", stdout.strip
    end
  end

  def test_permitted_hosts_can_be_configured_from_env
    with_env("PI_GATEWAY_PERMITTED_HOSTS" => "remote-workspace.tail8fd8b2.ts.net, example.test") do
      hosts = PiWebGateway.settings.host_authorization.fetch(:permitted_hosts)

      assert_includes hosts, "remote-workspace.tail8fd8b2.ts.net"
      assert_includes hosts, "example.test"
    end
  end

  def test_configured_host_passes_host_authorization
    env = ENV.to_h.merge(
      "PI_GATEWAY_ADMIN_PASSWORD" => "secret",
      "PI_GATEWAY_ENV_PATH" => File.join(Dir.tmpdir, "missing-pi-web-gateway-env"),
      "RACK_ENV" => "development",
      "APP_ENV" => "development",
      "PI_GATEWAY_PERMITTED_HOSTS" => "remote-workspace.tail8fd8b2.ts.net"
    )

    stdout, stderr, status = Open3.capture3(env, RbConfig.ruby, "-I.", "-e", <<~RUBY)
      require './app'
      require 'rack/mock'
      response = Rack::MockRequest.new(PiWebGateway).get('/', 'HTTP_HOST' => 'remote-workspace.tail8fd8b2.ts.net')
      puts response.status
      puts response.body
    RUBY

    assert status.success?, stderr
    assert_includes stdout, "Browser access required"
  end

  def test_session_view_links_to_notification_test
    Dir.mktmpdir do |dir|
      session_path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => session_path })

      assert_equal 200, response.status
      assert_includes response.body, 'data-notification-toggle'
      assert_includes response.body, 'aria-label="Enable notifications"'
      assert_includes response.body, 'class="sidebar-notification-label">Notifications</span>'
      assert_includes response.body, 'data-notification-toggle-state>Enable</span>'
      enabled_state = 'if (notificationsEnabled()) return { name: "enabled", label: "On"'
      blocked_state = 'if (!desktopNotificationAvailable() && ("Notification" in window) && Notification.permission === "denied")'
      assert_includes APP_JAVASCRIPT, enabled_state
      assert_includes APP_JAVASCRIPT, blocked_state
      assert_operator APP_JAVASCRIPT.index(enabled_state), :<, APP_JAVASCRIPT.index(blocked_state)
      refute_includes response.body, ">Notifications</a>"
    end
  end

  def test_serves_notification_test_page
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/notification-test")

      assert_equal 200, response.status
      assert_includes response.body, "Notification test"
      assert_includes response.body, "navigator.serviceWorker.register"
      assert_includes response.body, "Notification.requestPermission"
      assert_includes response.body, "piGatewayElectron"
      assert_includes response.body, "worker.active.postMessage"
      refute_includes response.body, "iPhone"
      refute_includes response.body, "iOS"
    end
  end

  def test_serves_web_app_manifest
    response = Rack::MockRequest.new(PiWebGateway).get("/manifest.webmanifest")

    assert_equal 200, response.status
    assert_equal "application/manifest+json", response.media_type
    manifest = JSON.parse(response.body)
    assert_equal "Pi Web Gateway", manifest.fetch("name")
    assert_equal "/", manifest.fetch("start_url")
    assert_equal "standalone", manifest.fetch("display")
  end

  def test_serves_service_worker
    response = Rack::MockRequest.new(PiWebGateway).get("/service-worker.js")

    assert_equal 200, response.status
    assert_equal "application/javascript", response.media_type
    assert_includes response.body, "self.registration.showNotification"
    assert_includes response.body, '["pi-notification", "pi-notification-test"].includes(data.type)'
    assert_includes response.body, "notificationclick"
  end

  def test_serves_public_frontend_assets_with_revalidation
    PiWebGateway.set :gateway_admin_password, "secret"
    request = Rack::MockRequest.new(PiWebGateway)

    css_response = request.get("/assets/app.css")
    js_response = request.get("/assets/app.js")

    assert_equal 200, css_response.status
    assert_equal "text/css", css_response.media_type
    assert_equal "no-cache", css_response["Cache-Control"]
    assert_includes css_response.body, ":root {"
    assert_equal 200, js_response.status
    assert_equal "text/javascript", js_response.media_type
    assert_equal "no-cache", js_response["Cache-Control"]
    assert_includes js_response.body, "function bindSessionDom()"

    revalidated_response = request.get("/assets/app.js", "HTTP_IF_MODIFIED_SINCE" => js_response["Last-Modified"])

    assert_equal 304, revalidated_response.status
    assert_empty revalidated_response.body
  end

  def test_serves_every_relative_module_import_reachable_from_the_app_entrypoint
    public_root = Pathname.new(PiWebGateway.settings.public_folder).expand_path
    pending = [public_root.join("assets/app.js")]
    visited = []
    request = Rack::MockRequest.new(PiWebGateway)

    until pending.empty?
      module_path = pending.shift.cleanpath
      next if visited.include?(module_path)

      assert module_path.to_s.start_with?("#{public_root}/"), "module import escapes public root: #{module_path}"
      assert module_path.file?, "module import does not resolve: #{module_path}"

      asset_path = "/#{module_path.relative_path_from(public_root)}"
      response = request.get(asset_path)
      assert_equal 200, response.status, "#{asset_path} is not served"
      assert_equal "text/javascript", response.media_type, "#{asset_path} is not served as JavaScript"
      visited << module_path

      relative_imports = response.body.scan(/^\s*(?:import|export)\s+(?:[^"']+?\s+from\s+)?["'](\.[^"']+)["']/m).flatten
      relative_imports.concat(response.body.scan(/\bimport\(\s*["'](\.[^"']+)["']\s*\)/).flatten)
      pending.concat(relative_imports.map { |specifier| module_path.dirname.join(specifier) })
    end

    assert_operator visited.length, :>, 1
  end

  def test_page_loads_frontend_assets_and_exposes_home_dir_as_data
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })
      document = Nokogiri::HTML(response.body)

      assert_equal 200, response.status
      asset_version = PiWebGateway.settings.gateway_instance_id
      assert_equal "/assets/app.css?v=#{asset_version}", document.at_css('head link[rel="stylesheet"]')["href"]
      assert_equal Dir.home, document.at_css("body")["data-home-dir"]
      script = document.at_css("body > script:last-child")
      assert_equal "/assets/app.js?v=#{asset_version}", script["src"]
      assert_equal "module", script["type"]
      refute_includes response.body, "const HOME_DIR"
      refute_includes response.body, "<style"
      refute_includes response.body, "<script>"
    end
  end

  def test_unknown_browser_sees_access_gate_when_admin_password_is_configured
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 403, response.status
      assert_includes response.body, "Browser access required"
      assert_includes response.body, "Ask access"
      assert_includes response["Set-Cookie"], "max-age=31536000"
      state = JSON.parse(File.read(PiWebGateway.settings.browser_access_path))
      assert_equal 1, state.fetch("pending_requests").length
      refute state.fetch("pending_requests").first.fetch("requested")
    end
  end

  def test_browser_auth_disabled_opens_single_user_gateway_without_browser_approval
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, nil
      PiWebGateway.set :browser_auth_disabled, true

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 200, response.status
      assert_includes response.body, "Pi Web Gateway"
      refute_includes response.body, "Browser access required"
      refute File.exist?(PiWebGateway.settings.browser_access_path)
    end
  end

  def test_browser_can_request_access_and_approved_browser_can_allow_it
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      request = Rack::MockRequest.new(PiWebGateway)

      blocked = request.get("/")
      cookie = Array(blocked["Set-Cookie"]).first.split(";", 2).first
      requested = request.post("/browser-access/request", "HTTP_COOKIE" => cookie)

      assert_equal 303, requested.status
      state = JSON.parse(File.read(PiWebGateway.settings.browser_access_path))
      pending = state.fetch("pending_requests").first
      assert pending.fetch("requested")

      store = BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path)
      approved_token = "approved-token"
      store.approve_current_browser(approved_token, label: "test")
      pending_response = request.get("/browser-access/pending", "HTTP_COOKIE" => "pi_gateway_browser=#{approved_token}")
      assert_equal 200, pending_response.status
      assert_equal pending.fetch("code"), JSON.parse(pending_response.body).fetch("requests").first.fetch("code")

      approve_response = request.post(
        "/browser-access/approve",
        params: { "code" => pending.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=#{approved_token}"
      )
      assert_equal 200, approve_response.status

      allowed = request.get("/", "HTTP_COOKIE" => cookie)
      assert_equal 200, allowed.status
      assert_includes allowed.body, "Pi Web Gateway"
      refute_includes allowed.body, "Browser access required"
    end
  end

  def test_stale_pending_browser_can_request_access_with_one_click
    old_time = (Time.now.utc - (31 * 24 * 60 * 60)).iso8601
    File.write(
      PiWebGateway.settings.browser_access_path,
      JSON.pretty_generate(
        "approved_browsers" => [],
        "pending_requests" => [
          { "code" => "OLD1", "token" => "stale-token", "requested" => false, "created_at" => old_time, "requested_at" => nil }
        ]
      ) + "\n"
    )
    PiWebGateway.set :gateway_admin_password, "secret"
    request = Rack::MockRequest.new(PiWebGateway)

    response = request.post("/browser-access/request", "HTTP_COOKIE" => "pi_gateway_browser=stale-token")

    assert_equal 303, response.status
    state = JSON.parse(File.read(PiWebGateway.settings.browser_access_path))
    pending = state.fetch("pending_requests").first
    assert_equal "stale-token", pending.fetch("token")
    assert pending.fetch("requested")
    refute_equal "OLD1", pending.fetch("code")
  end

  def test_access_redirects_reject_external_return_targets
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      request = Rack::MockRequest.new(PiWebGateway)

      blocked = request.get("/")
      cookie = Array(blocked["Set-Cookie"]).first.split(";", 2).first
      login = request.post(
        "/browser-access/admin-login",
        params: { "password" => "secret", "return_to" => "https://example.test/steal" },
        "HTTP_COOKIE" => cookie
      )
      assert_equal "http://example.org/", login["Location"]

      request_access = request.post(
        "/browser-access/request",
        params: { "return_to" => "//example.test/steal" },
        "HTTP_COOKIE" => cookie
      )
      assert_equal "http://example.org/", request_access["Location"]
    end
  end

  def test_approve_and_deny_require_code
    Dir.mktmpdir do |dir|
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      store = BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path)
      approved_token = "approved-token"
      store.approve_current_browser(approved_token, label: "test")
      request = Rack::MockRequest.new(PiWebGateway)

      approve_response = request.post("/browser-access/approve", "HTTP_COOKIE" => "pi_gateway_browser=#{approved_token}")
      deny_response = request.post("/browser-access/deny", "HTTP_COOKIE" => "pi_gateway_browser=#{approved_token}")

      assert_equal 400, approve_response.status
      assert_equal 400, deny_response.status
    end
  end

  def test_admin_password_approves_current_browser
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      request = Rack::MockRequest.new(PiWebGateway)

      blocked = request.get("/")
      cookie = Array(blocked["Set-Cookie"]).first.split(";", 2).first
      login = request.post(
        "/browser-access/admin-login",
        params: { "password" => "secret" },
        "HTTP_COOKIE" => cookie
      )
      assert_equal 303, login.status

      allowed = request.get("/", "HTTP_COOKIE" => cookie)
      assert_equal 200, allowed.status
      refute_includes allowed.body, "Browser access required"
    end
  end

  def test_posts_prompt_to_selected_session_and_redirects_back
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Hello Pi" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :prompt, "Hello Pi" ]], calls
      assert_includes response["Location"], Rack::Utils.escape(path)
    end
  end

  def test_json_prompt_redirect_does_not_preserve_temporary_sidebar_expansion
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Hello Pi", "expanded_cwd" => [project_cwd(dir)], "show_all_sessions" => "1" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(path)
      refute_includes payload.fetch("redirect"), "expanded_cwd"
      refute_includes payload.fetch("redirect"), "show_all_sessions=1"
    end
  end

  def test_json_prompt_returns_rpc_failure_without_recording_prompt
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        client = FakeRpcClient.new(calls)
        client.define_singleton_method(:prompt) do |message, images = []|
          calls << (images.empty? ? [:prompt, message] : [:prompt, message, images])
          { "type" => "response", "command" => "prompt", "success" => false, "error" => "No API key found for the selected model." }
        end
        client
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Hello Pi" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 422, response.status
      assert_equal [[ :start, path ], [ :prompt, "Hello Pi" ]], calls
      payload = JSON.parse(response.body)
      assert_equal false, payload.fetch("success")
      assert_equal "No API key found for the selected model.", payload.fetch("error")
      refute_includes File.read(path), "Hello Pi"
    end
  end

  def test_name_slash_command_renames_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/name Useful name" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :set_session_name, "Useful name" ]], calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "rename", payload.fetch("command")
      assert_equal "Useful name", payload.fetch("name")
    end
  end

  def test_rename_slash_command_renames_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/rename Useful name" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :set_session_name, "Useful name" ]], calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "rename", payload.fetch("command")
    end
  end

  def test_compact_slash_command_compacts_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/compact recent work" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :compact, "recent work" ]], calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "compact", payload.fetch("command")
    end
  end

  def test_bare_compact_slash_command_compacts_without_instructions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/compact" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :compact, nil ]], calls
      assert_equal "compact", JSON.parse(response.body).fetch("command")
    end
  end

  def test_fork_slash_command_returns_fork_command_without_rpc
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/fork" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_empty calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "fork", payload.fetch("command")
    end
  end

  def test_model_slash_command_returns_model_command_without_rpc
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/model" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_empty calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "model", payload.fetch("command")
    end
  end

  def test_tree_slash_command_returns_tree_command_without_rpc
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/tree" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_empty calls
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_equal "tree", payload.fetch("command")
    end
  end

  def test_new_slash_command_starts_session_in_selected_session_cwd
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(File.dirname(path), "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/new" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal new_path, payload.fetch("session")
      assert_equal "new", payload.fetch("command")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(new_path)
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
    end
  end

  def test_clone_slash_command_clones_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      cloned_path = File.join(File.dirname(path), "cloned.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], cloned_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/clone" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :clone_session ], [ :get_state ]], calls
      payload = JSON.parse(response.body)
      assert_equal cloned_path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(cloned_path)
    end
  end

  def test_multiline_compact_like_prompt_is_sent_as_prompt
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/compact recent\nwork" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :prompt, "/compact recent\nwork" ]], calls
      refute JSON.parse(response.body).key?("command")
    end
  end

  def test_bare_rename_slash_command_returns_usage_without_prompting
    ["/name", "/rename"].each do |message|
      Dir.mktmpdir do |dir|
        path = write_session(dir)
        calls = []
        PiWebGateway.set :sessions_root, dir
        PiWebGateway.set :rpc_client_factory, [->(session_path) {
          calls << [:start, session_path]
          FakeRpcClient.new(calls)
        }]

        response = Rack::MockRequest.new(PiWebGateway).post(
          "/prompt",
          params: { "session" => path, "message" => message },
          "HTTP_ACCEPT" => "application/json"
        )

        assert_equal 200, response.status
        assert_empty calls
        payload = JSON.parse(response.body)
        assert_equal path, payload.fetch("session")
        assert_equal "rename", payload.fetch("command")
        assert_equal "Usage: #{message} <name>", payload.fetch("error")
      end
    end
  end

  def test_multiline_rename_like_prompt_is_sent_as_prompt
    ["/rename Useful\nname", "/rename\nUseful name"].each do |message|
      Dir.mktmpdir do |dir|
        path = write_session(dir)
        calls = []
        PiWebGateway.set :sessions_root, dir
        PiWebGateway.set :rpc_client_registry, nil
        PiWebGateway.set :rpc_client_factory, [->(session_path) {
          calls << [:start, session_path]
          FakeRpcClient.new(calls)
        }]

        response = Rack::MockRequest.new(PiWebGateway).post(
          "/prompt",
          params: { "session" => path, "message" => message },
          "HTTP_ACCEPT" => "application/json"
        )

        assert_equal 200, response.status
        assert_equal [[ :start, path ], [ :prompt, message ]], calls
        refute JSON.parse(response.body).key?("command")
      end
    end
  end

  def test_posts_follow_up_prompt_to_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Actually do this", "streaming_behavior" => "follow_up" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :follow_up, "Actually do this" ]], calls
      assert_equal true, JSON.parse(response.body).fetch("follow_up")
    end
  end

  def test_follow_up_prompt_treats_rename_slash_command_as_message
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "/name keep steering", "streaming_behavior" => "follow_up" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :follow_up, "/name keep steering" ]], calls
      payload = JSON.parse(response.body)
      assert_equal true, payload.fetch("follow_up")
      refute payload.key?("command")
    end
  end

  def test_follow_up_prompt_treats_session_control_slash_commands_as_messages
    ["/fork", "/tree", "/clone", "/new", "/model"].each do |message|
      Dir.mktmpdir do |dir|
        path = write_session(dir)
        calls = []
        PiWebGateway.set :sessions_root, dir
        PiWebGateway.set :rpc_client_registry, nil
        PiWebGateway.set :rpc_client_factory, [->(session_path) {
          calls << [:start, session_path]
          FakeRpcClient.new(calls)
        }]

        response = Rack::MockRequest.new(PiWebGateway).post(
          "/prompt",
          params: { "session" => path, "message" => message, "streaming_behavior" => "follow_up" },
          "HTTP_ACCEPT" => "application/json"
        )

        assert_equal 200, response.status
        assert_equal [[ :start, path ], [ :follow_up, message ]], calls
        payload = JSON.parse(response.body)
        assert_equal true, payload.fetch("follow_up")
        refute payload.key?("command")
      end
    end
  end

  def test_posts_follow_up_prompt_with_uploaded_images
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      image_path = File.join(dir, "screenshot.png")
      File.binwrite(image_path, "fake image data")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      upload = Rack::Multipart::UploadedFile.new(image_path, "image/png", true)
      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "Actually do this", "streaming_behavior" => "follow_up", "images[]" => upload },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [:start, path], calls[0]
      assert_equal :follow_up, calls[1][0]
      assert_match %r{\AActually do this\n\n/.+/[a-f0-9]{64}/[a-f0-9]{64}\.png\z}, calls[1][1]
      assert_equal [{ type: "image", data: Base64.strict_encode64("fake image data"), mimeType: "image/png" }], calls[1][2]
      assert_equal true, JSON.parse(response.body).fetch("follow_up")
    end
  end

  def test_posts_prompt_with_uploaded_images
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      image_path = File.join(dir, "screenshot.png")
      File.binwrite(image_path, "fake image data")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      upload = Rack::Multipart::UploadedFile.new(image_path, "image/png", true)
      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "What is this?", "images[]" => upload }
      )

      assert_equal 303, response.status
      assert_equal [:start, path], calls[0]
      assert_equal :prompt, calls[1][0]
      assert_match %r{\AWhat is this\?\n\n/.+/[a-f0-9]{64}/[a-f0-9]{64}\.png\z}, calls[1][1]
      assert_equal [{ type: "image", data: Base64.strict_encode64("fake image data"), mimeType: "image/png" }], calls[1][2]
    end
  end

  def test_renders_historical_uploaded_image_prompt
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      image_path = File.join(dir, "screenshot.png")
      File.binwrite(image_path, "fake image data")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      upload = Rack::Multipart::UploadedFile.new(image_path, "image/png", true)
      post_response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "What is this?", "images[]" => upload }
      )
      File.open(path, "a") do |file|
        file.puts(JSON.generate(
          type: "message",
          timestamp: Time.now.utc.iso8601,
          message: { role: "user", content: [{ type: "text", text: calls[1][1] }] }
        ))
      end

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 303, post_response.status
      assert_equal 200, response.status
      assert_includes response.body, "message-images"
      assert_includes response.body, "/attachments/"
      refute_includes response.body, "📎 1 image attachment"

      attachment_url = response.body.match(%r{/attachments/[a-f0-9]{64}/[a-f0-9]{64}\.png})[0]
      attachment_response = Rack::MockRequest.new(PiWebGateway).get(attachment_url)
      assert_equal 200, attachment_response.status
      assert_equal "fake image data", attachment_response.body
    end
  end

  def test_renders_jsonl_image_only_user_message
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      image_data = Base64.strict_encode64("fake image data")
      PiWebGateway.set :sessions_root, dir
      File.open(path, "a") do |file|
        file.puts(JSON.generate(
          type: "message",
          timestamp: Time.now.utc.iso8601,
          message: { role: "user", content: [{ type: "image", data: image_data, mimeType: "image/png" }] }
        ))
      end

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "message-images"
      assert_includes response.body, "data:image/png;base64,#{image_data}"
    end
  end

  def test_renders_markdown_endpoint_with_sanitization_for_live_messages
    response = Rack::MockRequest.new(PiWebGateway).post(
      "/markdown",
      params: { "text" => "## Live\n\n<script>alert('x')</script><a href=\"javascript:alert(1)\">bad</a>" }
    )

    assert_equal 200, response.status
    payload = JSON.parse(response.body)
    assert_includes payload["html"], "<h2>Live</h2>"
    refute_includes payload["html"], "<script>"
    refute_includes payload["html"], "javascript:alert"
  end

  def test_markdown_repeated_one_items_render_as_single_ordered_list
    response = Rack::MockRequest.new(PiWebGateway).post(
      "/markdown",
      params: { "text" => "1. First\n1. Second\n1. Third" }
    )

    assert_equal 200, response.status
    html = JSON.parse(response.body).fetch("html")
    assert_equal 1, html.scan("<ol").length
    assert_includes html, "<li>First</li>"
    assert_includes html, "<li>Second</li>"
    assert_includes html, "<li>Third</li>"
  end

  def test_markdown_continues_ordered_lists_across_code_blocks
    response = Rack::MockRequest.new(PiWebGateway).post(
      "/markdown",
      params: { "text" => "1. First\n1. Second\n1. Third\n\n```ruby\nputs :code\n```\n\n1. Fourth\n\n```ruby\nputs :more\n```\n\n1. Fifth" }
    )

    assert_equal 200, response.status
    html = JSON.parse(response.body).fetch("html")
    assert_includes html, "<ol>\n<li>First</li>\n<li>Second</li>\n<li>Third</li>\n</ol>"
    assert_includes html, "<ol start=\"4\">\n<li>Fourth</li>\n</ol>"
    assert_includes html, "<ol start=\"5\">\n<li>Fifth</li>\n</ol>"
  end

  def test_markdown_highlights_fenced_code_blocks_safely
    response = Rack::MockRequest.new(PiWebGateway).post(
      "/markdown",
      params: { "text" => "```ruby\nputs :ok\n<script>alert('x')</script>\n```\n\n```unknown\n<bad>\n```" }
    )

    assert_equal 200, response.status
    html = JSON.parse(response.body).fetch("html")
    assert_includes html, "<code class=\"highlight ruby\">"
    assert_includes html, "<span class=\"syntax-function\">puts</span>"
    assert_includes html, "&lt;script&gt;alert("
    assert_includes html, "&lt;/script&gt;"
    assert_includes html, "<code class=\"unknown\">&lt;bad&gt;"
    refute_includes html, "<script>"
    refute_includes html, "<bad>"
  end

  def test_markdown_highlights_common_fenced_code_language_aliases
    markdown = <<~MARKDOWN
      ```js
      const value = true
      ```

      ```json
      {"ok": true}
      ```

      ```bash
      echo "$HOME"
      ```
    MARKDOWN
    response = Rack::MockRequest.new(PiWebGateway).post("/markdown", params: { "text" => markdown })

    assert_equal 200, response.status
    html = JSON.parse(response.body).fetch("html")
    assert_includes html, "<code class=\"highlight javascript\">"
    assert_includes html, "<span class=\"syntax-keyword\">const</span>"
    assert_includes html, "<code class=\"highlight json\">"
    assert_includes html, "<span class=\"syntax-key\">\"ok\"</span>"
    assert_includes html, "<code class=\"highlight shell\">"
    assert_includes html, "<span class=\"syntax-function\">echo</span>"
  end

  def test_returns_buffered_rpc_events_for_selected_session_cursor
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      client = FakeRpcClient.new(calls, [{ "type" => "assistant_delta", "text" => "Hi" }])
      registry = PiRpcClientRegistry.new(factory: ->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      })
      registry.register(path, client)
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => path, "after" => "0" }
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      assert_event_payload(response, events: [{ "type" => "assistant_delta", "text" => "Hi" }], last_seq: 1, mode: "managed")
      assert_equal [[ :events_after, 0 ]], calls
    end
  end

  def test_returns_same_buffered_rpc_events_to_independent_cursors
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      client = FakeRpcClient.new(calls, [{ "type" => "assistant_delta", "text" => "Hi" }])
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :rpc_client_registry, registry

      request = Rack::MockRequest.new(PiWebGateway)
      first = request.get("/events", params: { "session" => path, "after" => "0" })
      second = request.get("/events", params: { "session" => path, "after" => "0" })

      assert_equal JSON.parse(first.body), JSON.parse(second.body)
      assert_event_payload(second, events: [{ "type" => "assistant_delta", "text" => "Hi" }], last_seq: 1, mode: "managed")
      assert_equal [[ :events_after, 0 ], [ :events_after, 0 ]], calls
    end
  end

  def test_ignores_event_polls_for_inactive_sessions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      other_path = File.join(File.dirname(path), "other-session.jsonl")
      registry = PiRpcClientRegistry.new(factory: ->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      })
      registry.register(other_path, FakeRpcClient.new(calls, [{ "type" => "stale" }]))
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_event_payload(response, events: [], last_seq: 0, mode: "available")
      assert_empty calls
    end
  end

  def test_keeps_rpc_clients_isolated_when_prompting_multiple_sessions
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      paths.each do |path|
        response = Rack::MockRequest.new(PiWebGateway).post(
          "/prompt",
          params: { "session" => path, "message" => "Hello #{File.basename(path)}" }
        )
        assert_equal 303, response.status
      end

      assert_equal [
        [:start, paths.first],
        [:prompt, "Hello session-1.jsonl"],
        [:start, paths.last],
        [:prompt, "Hello session-2.jsonl"]
      ], calls
      refute_includes calls, [:close]
    end
  end

  def test_reads_events_from_each_registered_session_without_cross_talk
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(paths.first, FakeRpcClient.new(calls, [{ "type" => "from-a" }]))
      registry.register(paths.last, FakeRpcClient.new(calls, [{ "type" => "from-b" }]))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      request = Rack::MockRequest.new(PiWebGateway)
      response_a = request.get("/events", params: { "session" => paths.first })
      response_b = request.get("/events", params: { "session" => paths.last })

      assert_event_payload(response_a, events: [{ "type" => "from-a" }], last_seq: 1, mode: "managed")
      assert_event_payload(response_b, events: [{ "type" => "from-b" }], last_seq: 1, mode: "managed")
    end
  end

  def test_creating_new_session_does_not_close_or_relabel_parent_client
    Dir.mktmpdir do |dir|
      parent_path = write_session(dir)
      new_path = File.join(File.dirname(parent_path), "new-session.jsonl")
      calls = []
      parent_client = FakeRpcClient.new(calls)
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(parent_path, parent_client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => parent_path }
      )

      assert_equal 303, response.status
      assert_same parent_client, registry.client_for(parent_path)
      refute_includes calls, [:close]
      refute_includes calls, [:new_session, parent_path]
      assert_equal [[:start_new, project_cwd(dir)], [:get_state]], calls
    end
  end

  def test_creates_new_native_session_and_redirects_to_it
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(File.dirname(path), "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => path }
      )

      assert_equal 303, response.status
      assert_includes response["Location"], Rack::Utils.escape(new_path)
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
    end
  end

  def test_creates_pending_session_when_new_client_has_not_persisted_session_file
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => paths.first }
      )

      assert_equal 303, response.status
      assert_match %r{pending-[^&]+\.jsonl}, response["Location"]
      refute_includes response["Location"], Rack::Utils.escape(paths.last)
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
    end
  end

  def test_creates_new_native_session_from_pending_session_cwd
    Dir.mktmpdir do |dir|
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => pending_path }
      )

      assert_equal 303, response.status
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
    end
  end

  def test_creates_new_native_session_as_json
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(File.dirname(path), "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new",
        params: { "session" => path, "expanded_cwd" => [project_cwd(dir)], "show_all_sessions" => "1" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      payload = JSON.parse(response.body)
      assert_equal new_path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(new_path)
      refute_includes payload.fetch("redirect"), "expanded_cwd"
      refute_includes payload.fetch("redirect"), "show_all_sessions=1"
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
    end
  end

  def test_returns_fork_messages_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [{ "entryId" => "entry-1", "text" => "Try this" }])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/fork_messages",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [{ "entryId" => "entry-1", "text" => "Try this" }], JSON.parse(response.body).fetch("messages")
      assert_equal [[:start, path], [:get_fork_messages]], calls
    end
  end

  def test_returns_tree_entries_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "assistant", content: [{ type: "text", text: "First answer" }] } },
        { type: "message", id: "hidden-tool-call-only-assistant", parentId: "assistant-1", timestamp: "2026-06-13T10:01:10Z", message: { role: "assistant", stopReason: "toolUse", content: [{ type: "toolCall", name: "bash", arguments: { command: "ls" } }] } },
        { type: "message", id: "tool-call-only-assistant", parentId: "assistant-1", timestamp: "2026-06-13T10:01:15Z", message: { role: "assistant", stopReason: "toolUse", content: [{ type: "toolCall", name: "bash", arguments: { command: "pwd" } }] } },
        { type: "message", id: "tool-result-1", parentId: "assistant-1", timestamp: "2026-06-13T10:01:30Z", message: { role: "toolResult", content: [{ type: "text", text: "Tool output" }] } },
        { type: "label", id: "label-1", parentId: "user-1", timestamp: "2026-06-13T10:01:45Z", targetId: "user-1", label: "checkpoint" },
        { type: "message", id: "user-2", parentId: "label-1", timestamp: "2026-06-13T10:02:00Z", message: { role: "user", content: [{ type: "text", text: "Alternate prompt" }] } }
      ])
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], "tool-call-only-assistant")
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/tree_entries",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      entries = JSON.parse(response.body).fetch("entries")
      assert_equal ["user-1", "assistant-1", "tool-call-only-assistant", "tool-result-1", "user-2"], entries.map { |entry| entry.fetch("entryId") }
      assert_equal [0, 1, 2, 2, 2], entries.map { |entry| entry.fetch("depth") }
      assert_equal ["user", "assistant", "assistant", "toolResult", "user"], entries.map { |entry| entry.fetch("role") }
      assert_equal "Alternate prompt", entries.last.fetch("text")
      assert_equal "Alternate prompt", entries.last.fetch("editorText")
      refute entries[1].key?("editorText")
      assert_equal [[:start, path], [:tree_leaf]], calls
    end
  end

  def test_session_store_messages_can_follow_selected_tree_leaf
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "assistant", content: [{ type: "text", text: "First answer" }] } },
        { type: "message", id: "user-2", parentId: "assistant-1", timestamp: "2026-06-13T10:02:00Z", message: { role: "user", content: [{ type: "text", text: "Later prompt" }] } },
        { type: "message", id: "assistant-2", parentId: "user-2", timestamp: "2026-06-13T10:03:00Z", message: { role: "assistant", content: [{ type: "text", text: "Later answer" }] } }
      ])

      messages = PiSessionStore.new(root: dir).messages(path, current_leaf_id: "assistant-1")

      assert_equal ["First prompt", "First answer"], messages.map(&:text)
    end
  end

  def test_sidebar_refresh_does_not_query_active_tree_leaf
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [{ role: "user", text: "Hello" }])
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]
      request = Rack::MockRequest.new(PiWebGateway)

      request.post("/sessions/tree", params: { "session" => path, "entry_id" => "entry-1" }, "HTTP_ACCEPT" => "application/json")
      response = request.get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal [[:start, path], [:navigate_tree, "entry-1"]], calls
    end
  end

  def test_follows_external_pi_cli_entries_blocks_prompts_and_allows_takeover
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "Gateway prompt" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "assistant", content: [{ type: "text", text: "Gateway answer" }] } }
      ])
      stale_calls = []
      fresh_calls = []
      stale_client = SyncAwareRpcClient.new(["user-1", "assistant-1"], "assistant-1", stale_calls)
      stale_client.busy = true
      fresh_client = SyncAwareRpcClient.new(["user-1", "assistant-1", "pi-cli-user"], "pi-cli-user", fresh_calls)
      now = Time.at(1_000)
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { fresh_client }, clock: -> { now })
      registry.register(path, stale_client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      request = Rack::MockRequest.new(PiWebGateway)

      initial = request.get("/", params: { "session" => path })
      assert_equal 200, initial.status
      refute_includes initial.body, "Session changed outside the gateway"

      File.open(path, "a") do |file|
        file.puts(JSON.generate(
          type: "message",
          id: "pi-cli-user",
          parentId: "user-1",
          timestamp: "2026-06-13T10:02:00Z",
          message: { role: "user", content: [{ type: "text", text: "From Pi CLI" }] }
        ))
      end

      events = request.get("/events", params: { "session" => path })
      events_payload = JSON.parse(events.body)
      assert_equal "external_follow", events_payload.dig("session_sync", "mode")
      refute_includes stale_calls, [:close]

      stale_client.busy = false
      stale_client.events = [{ "type" => "agent_settled" }]
      now = Time.at(3_000)
      stale_client.settled_at = now
      sidebar_response = request.get("/sidebar", params: { "session" => path })
      assert_equal 200, sidebar_response.status
      refute_includes stale_calls, [:close]
      settled_events = JSON.parse(request.get("/events", params: { "session" => path }).body)
      assert_equal [{ "type" => "agent_settled" }], settled_events.fetch("events")
      assert_equal false, settled_events.dig("session_sync", "gateway_busy")
      assert_includes stale_calls, [:close]

      fragment = request.get("/session_fragment", params: { "session" => path })
      conversation_html = JSON.parse(fragment.body).fetch("conversation_html")
      assert_includes conversation_html, "From Pi CLI"
      refute_includes conversation_html, "Gateway answer"
      assert_includes conversation_html, "Session changed outside the gateway"
      assert_includes conversation_html, "Finish using this session in Pi CLI"
      assert_includes conversation_html, "data-session-takeover"
      assert_match(/<textarea[^>]+disabled/, conversation_html)

      image_path = File.join(dir, "blocked.png")
      File.binwrite(image_path, "blocked image")
      upload = Rack::Multipart::UploadedFile.new(image_path, "image/png", true)
      rejected_prompt = request.post(
        "/prompt",
        params: { "session" => path, "message" => "Unsafe prompt", "images[]" => upload },
        "HTTP_ACCEPT" => "application/json"
      )
      assert_equal 409, rejected_prompt.status
      assert_empty Dir.glob(File.join(@attachments_root, "**", "*"))

      blocked_requests = [
        [:get, "/commands", { "session" => path }],
        [:get, "/sessions/model_settings", { "session" => path }],
        [:get, "/sessions/fork_messages", { "session" => path }],
        [:get, "/sessions/tree_entries", { "session" => path }],
        [:post, "/sessions/model_settings", { "session" => path, "provider" => "openai", "model" => "gpt-5", "thinking" => "high" }],
        [:post, "/sessions/cycle_thinking", { "session" => path }],
        [:post, "/sessions/tree", { "session" => path, "entry_id" => "user-1" }],
        [:post, "/sessions/fork", { "session" => path, "entry_id" => "user-1" }],
        [:post, "/sessions/clone", { "session" => path }],
        [:post, "/compact", { "session" => path }],
        [:post, "/rename", { "session" => path, "name" => "Blocked rename" }],
        [:post, "/abort", { "session" => path }]
      ]
      blocked_requests.each do |method, endpoint, request_params|
        response = request.public_send(method, endpoint, params: request_params, "HTTP_ACCEPT" => "application/json")
        assert_equal 409, response.status, "expected #{method.upcase} #{endpoint} to be blocked"
      end
      assert_empty fresh_calls

      takeover = request.post(
        "/sessions/takeover",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )
      assert_equal 200, takeover.status
      assert_equal "managed", JSON.parse(takeover.body).dig("session_sync", "mode")

      resumed_prompt = request.post(
        "/prompt",
        params: { "session" => path, "message" => "Continue in gateway" },
        "HTTP_ACCEPT" => "application/json"
      )
      assert_equal 200, resumed_prompt.status
      assert_includes fresh_calls, [:prompt, "Continue in gateway"]
    end
  end

  def test_conversation_view_recovers_when_active_rpc_client_has_exited
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [{ role: "user", text: "Persisted prompt" }])
      calls = []
      dead_client = FakeRpcClient.new(calls)
      dead_client.define_singleton_method(:session_position) do |_append_cursor|
        calls << [:session_position]
        raise IOError, "Pi RPC process exited"
      end
      registry = PiRpcClientRegistry.new(factory: ->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      })
      registry.register(path, dead_client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "Persisted prompt"
      refute registry.active?(path)
      assert_equal [[:session_position], [:close]], calls

      abort_response = Rack::MockRequest.new(PiWebGateway).post("/abort", params: { "session" => path })

      assert_equal 303, abort_response.status
      assert_equal [[:session_position], [:close], [:start, path], [:abort]], calls
    end
  end

  def test_conversation_views_follow_active_tree_leaf
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "assistant", content: [{ type: "text", text: "First answer" }] } },
        { type: "message", id: "user-2", parentId: "assistant-1", timestamp: "2026-06-13T10:02:00Z", message: { role: "user", content: [{ type: "text", text: "Later prompt" }] } },
        { type: "message", id: "assistant-2", parentId: "user-2", timestamp: "2026-06-13T10:03:00Z", message: { role: "assistant", content: [{ type: "text", text: "Later answer" }] } }
      ])
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]
      request = Rack::MockRequest.new(PiWebGateway)

      request.post("/sessions/tree", params: { "session" => path, "entry_id" => "assistant-1" }, "HTTP_ACCEPT" => "application/json")
      fragment_response = request.get("/session_fragment", params: { "session" => path }, "HTTP_ACCEPT" => "application/json")
      page_response = request.get("/", params: { "session" => path })

      assert_equal 200, fragment_response.status
      conversation_html = JSON.parse(fragment_response.body).fetch("conversation_html")
      assert_includes conversation_html, "First prompt"
      assert_includes conversation_html, "First answer"
      assert_includes conversation_html, "Viewing earlier tree point"
      assert_includes conversation_html, "data-tree-latest-entry-id=\"assistant-2\""
      assert_includes conversation_html, "tree_leaf=assistant-1"
      refute_includes conversation_html, "Later prompt"
      refute_includes conversation_html, "Later answer"
      assert_equal 200, page_response.status
      page_conversation_text = Nokogiri::HTML(page_response.body).at_css(".conversation-panel").text
      assert_includes page_conversation_text, "First prompt"
      assert_includes page_conversation_text, "First answer"
      assert_includes page_conversation_text, "Viewing earlier tree point"
      refute_includes page_conversation_text, "Later prompt"
      refute_includes page_conversation_text, "Later answer"
      assert_equal [[:start, path], [:navigate_tree, "assistant-1"]], calls
    end
  end

  def test_tree_entries_mark_current_and_latest_positions
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-1", parentId: nil, timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "First prompt" }] } },
        { type: "message", id: "assistant-1", parentId: "user-1", timestamp: "2026-06-13T10:01:00Z", message: { role: "assistant", content: [{ type: "text", text: "First answer" }] } },
        { type: "message", id: "assistant-2", parentId: "assistant-1", timestamp: "2026-06-13T10:03:00Z", message: { role: "assistant", content: [{ type: "text", text: "Later answer" }] } }
      ])
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], "assistant-1")
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/tree_entries",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      entries = JSON.parse(response.body).fetch("entries")
      assert_equal [false, true, false], entries.map { |entry| entry.fetch("current") }
      assert_equal [false, false, true], entries.map { |entry| entry.fetch("latest") }
      assert_equal [[:start, path], [:tree_leaf]], calls
    end
  end

  def test_navigates_session_tree_and_returns_json
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/tree",
        params: { "session" => path, "entry_id" => "entry-1" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(path)
      assert_equal [[:start, path], [:navigate_tree, "entry-1"]], calls
    end
  end

  def test_forks_session_and_returns_new_session_as_json
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      forked_path = File.join(File.dirname(path), "forked.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], forked_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/fork",
        params: { "session" => path, "entry_id" => "entry-1", "show_all_sessions" => "1" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal forked_path, payload.fetch("session")
      assert_equal "Forked prompt", payload.fetch("text")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(forked_path)
      refute_includes payload.fetch("redirect"), "show_all_sessions=1"
      assert_equal [[:start, path], [:fork, "entry-1"], [:get_state]], calls
    end
  end

  def test_pending_fork_session_remains_selectable_before_file_exists
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      forked_path = File.join(File.dirname(path), "pending-fork.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], forked_path)
      }]

      post_response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/fork",
        params: { "session" => path, "entry_id" => "entry-1" },
        "HTTP_ACCEPT" => "application/json"
      )
      assert_equal forked_path, JSON.parse(post_response.body).fetch("session")
      assert_equal project_cwd(dir), PiWebGateway.pending_session_registry.cwd_for(forked_path)

      fragment_response = Rack::MockRequest.new(PiWebGateway).get(
        "/session_fragment",
        params: { "session" => forked_path }
      )

      assert_equal 200, fragment_response.status
      payload = JSON.parse(fragment_response.body)
      assert_equal forked_path, payload.fetch("session")
      assert_includes payload.fetch("conversation_html"), "New session (pending first assistant response)"
    end
  end

  def test_clones_session_and_returns_new_session_as_json
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      cloned_path = File.join(File.dirname(path), "cloned.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [], cloned_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/clone",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal cloned_path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(cloned_path)
      assert_equal [[:start, path], [:clone_session], [:get_state]], calls
    end
  end

  def test_validates_new_session_cwd_as_json
    Dir.mktmpdir do |dir|
      cwd = File.join(dir, "project")
      FileUtils.mkdir_p(cwd)
      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/validate_cwd",
        params: { "cwd" => cwd },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal true, payload.fetch("valid")
      assert_equal File.realpath(cwd), payload.fetch("cwd")
    end
  end

  def test_rejects_invalid_new_session_cwd_as_json
    Dir.mktmpdir do |dir|
      missing = File.join(dir, "missing")
      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/validate_cwd",
        params: { "cwd" => missing },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 422, response.status
      payload = JSON.parse(response.body)
      assert_equal false, payload.fetch("valid")
      assert_includes payload.fetch("error"), "directory"
    end
  end

  def test_browses_children_of_an_existing_new_session_cwd
    Dir.mktmpdir do |dir|
      %w[beta alpha].each { |name| FileUtils.mkdir_p(File.join(dir, name)) }
      FileUtils.mkdir_p(File.join(dir, ".hidden"))
      File.write(File.join(dir, "notes.txt"), "not a directory")

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/browse_cwd",
        params: { "cwd" => dir },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal true, payload.fetch("valid")
      assert_equal File.realpath(dir), payload.fetch("cwd")
      assert_equal %w[alpha beta].map { |name| File.join(dir, name) }, payload.fetch("directories")
      assert_equal "no-store", response["cache-control"]
    end
  end

  def test_browsing_new_session_cwds_skips_names_that_cannot_be_shown_in_json
    Dir.mktmpdir do |dir|
      FileUtils.mkdir_p(File.join(dir, "visible"))
      FileUtils.mkdir_p(File.join(dir.b, "invalid-\xff".b))

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/browse_cwd",
        params: { "cwd" => dir },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [File.join(dir, "visible")], JSON.parse(response.body).fetch("directories")
    end
  end

  def test_browsing_new_session_cwds_rejects_malformed_path_encoding
    response = Rack::MockRequest.new(PiWebGateway).get(
      "/sessions/browse_cwd?cwd=%FF",
      "HTTP_ACCEPT" => "application/json"
    )

    assert_equal 200, response.status
    payload = JSON.parse(response.body)
    assert_equal false, payload.fetch("valid")
    assert_empty payload.fetch("directories")
  end

  def test_browses_new_session_cwds_by_the_typed_path_component
    Dir.mktmpdir do |dir|
      %w[alpha alpine beta .archive .config].each { |name| FileUtils.mkdir_p(File.join(dir, name)) }

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/browse_cwd",
        params: { "cwd" => File.join(dir, "al") },
        "HTTP_ACCEPT" => "application/json"
      )
      hidden_response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/browse_cwd",
        params: { "cwd" => File.join(dir, ".a") },
        "HTTP_ACCEPT" => "application/json"
      )

      payload = JSON.parse(response.body)
      assert_equal false, payload.fetch("valid")
      assert_equal [File.join(dir, "alpha"), File.join(dir, "alpine")], payload.fetch("directories")
      assert_equal [File.join(dir, ".archive")], JSON.parse(hidden_response.body).fetch("directories")
    end
  end

  def test_limits_new_session_cwd_browser_results
    Dir.mktmpdir do |dir|
      35.times { |index| FileUtils.mkdir_p(File.join(dir, format("project-%02d", index))) }

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sessions/browse_cwd",
        params: { "cwd" => File.join(dir, "project-") },
        "HTTP_ACCEPT" => "application/json"
      )

      directories = JSON.parse(response.body).fetch("directories")
      assert_equal 30, directories.length
      assert_equal File.join(dir, "project-00"), directories.first
      assert_equal File.join(dir, "project-29"), directories.last
    end
  end

  def test_creates_new_native_session_from_validated_cwd_as_json
    Dir.mktmpdir do |dir|
      cwd = File.join(dir, "new-project")
      FileUtils.mkdir_p(cwd)
      new_path = File.join(dir, "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(started_cwd) {
        calls << [:start_new, started_cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new_at_cwd",
        params: { "cwd" => cwd, "show_all_sessions" => "1" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal new_path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(new_path)
      refute_includes payload.fetch("redirect"), "show_all_sessions=1"
      assert_equal [[ :start_new, File.realpath(cwd) ], [ :get_state ]], calls
    end
  end

  def test_rejects_new_session_from_invalid_cwd_as_json
    Dir.mktmpdir do |dir|
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(started_cwd) {
        calls << [:start_new, started_cwd]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new_at_cwd",
        params: { "cwd" => File.join(dir, "missing") },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 422, response.status
      payload = JSON.parse(response.body)
      assert_equal false, payload.fetch("valid")
      assert_empty calls
    end
  end

  def test_event_poll_does_not_remap_pending_client_when_real_session_file_appears
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [{ "type" => "from-pending" }], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => real_path }
      )

      assert_equal 200, response.status
      assert_event_payload(response, events: [], last_seq: 0, mode: "available")
      refute registry.active?(real_path)
      assert registry.active?(pending_path)
      assert_includes PiWebGateway.pending_session_registry.paths, pending_path
      assert_empty calls
    end
  end

  def test_prompt_remaps_pending_client_when_real_session_file_appears
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => real_path, "message" => "Continue" }
      )

      assert_equal 303, response.status
      assert registry.active?(real_path)
      refute registry.active?(pending_path)
      refute_includes PiWebGateway.pending_session_registry.paths, pending_path
      assert_equal [[:get_state], [:prompt, "Continue"]], calls
    end
  end

  def test_prompt_with_pending_path_redirects_to_real_session_when_real_session_file_appears
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => pending_path, "message" => "Continue" }
      )

      assert_equal 303, response.status
      assert_includes response["Location"], Rack::Utils.escape(real_path)
      refute_includes response["Location"], Rack::Utils.escape(pending_path)
      assert registry.active?(real_path)
      refute registry.active?(pending_path)
      refute_includes PiWebGateway.pending_session_registry.paths, pending_path
      assert_equal [[:get_state], [:prompt, "Continue"]], calls
    end
  end

  def test_json_prompt_with_pending_path_returns_real_session_redirect
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        "HTTP_ACCEPT" => "application/json",
        params: { "session" => pending_path, "message" => "Continue" }
      )
      payload = JSON.parse(response.body)

      assert_equal 200, response.status
      assert_equal real_path, payload["session"]
      assert_includes payload["redirect"], Rack::Utils.escape(real_path)
      assert_equal [[:get_state], [:prompt, "Continue"]], calls
    end
  end

  def test_json_rename_with_pending_path_returns_real_session_redirect
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        "HTTP_ACCEPT" => "application/json",
        params: { "session" => pending_path, "message" => "/rename Lovely session" }
      )
      payload = JSON.parse(response.body)

      assert_equal 200, response.status
      assert_equal real_path, payload["session"]
      assert_includes payload["redirect"], Rack::Utils.escape(real_path)
      assert_equal "rename", payload["command"]
      assert_equal "Lovely session", payload["name"]
      assert_equal [[:get_state], [:set_session_name, "Lovely session"]], calls
    end
  end

  def test_hides_sessions_whose_cwd_no_longer_exists_without_deleting_them
    Dir.mktmpdir do |dir|
      stale_dir = File.join(dir, "--stale--")
      stale_cwd = File.join(dir, "deleted-worktree")
      FileUtils.mkdir_p(stale_dir)
      stale_path = File.join(stale_dir, "stale.jsonl")
      File.write(stale_path, JSON.generate({ type: "session", id: "stale", cwd: stale_cwd }) + "\n")
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]
      request = Rack::MockRequest.new(PiWebGateway)

      response = request.get("/")
      search_response = request.get("/sidebar", params: { "session_search" => "stale" })
      selected_response = request.get("/session_fragment", params: { "session" => stale_path })

      assert_equal 200, response.status
      assert File.exist?(stale_path)
      assert_includes response.body, path
      refute_includes response.body, "stale.jsonl"
      refute_includes search_response.body, "stale.jsonl"
      assert_equal path, JSON.parse(selected_response.body).fetch("session")

      FileUtils.mkdir_p(stale_cwd)
      restored_response = request.get("/sidebar", params: { "session_search" => "stale" })

      assert_includes restored_response.body, "stale.jsonl"
    end
  end

  def test_renders_lazy_command_discovery_placeholder_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [{ "name" => "review", "source" => "skill", "description" => "Review code" }])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Slash commands"
      assert_includes response.body, "data-commands-url"
      refute_includes response.body, "/review"
      assert_empty calls
    end
  end

  def test_loads_commands_on_demand
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [
          { "name" => "review", "source" => "skill", "description" => "Review code" },
          { "name" => "sessions", "source" => "extension", "description" => "Switch, rename, or delete project sessions" },
          { "name" => "rename", "source" => "extension", "description" => "Rename the current session" },
          { "name" => "pi_web_tree", "source" => "extension", "description" => "Internal bridge" },
          { "name" => "pi_web_tree_leaf", "source" => "extension", "description" => "Internal bridge" }
        ])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get("/commands", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "Slash commands (7)"
      assert_includes response.body, "/review"
      assert_includes response.body, "Review code"
      assert_includes response.body, "/compact"
      assert_includes response.body, "/fork"
      assert_includes response.body, "/tree"
      assert_includes response.body, "/clone"
      assert_includes response.body, "/new"
      assert_includes response.body, "/model"
      refute_includes response.body, "/sessions"
      refute_includes response.body, "/rename"
      refute_includes response.body, "pi_web_tree"
      refute_includes response.body, "pi_web_tree_leaf"
      refute_includes response.body, "command-filter"
      assert_equal [[ :start, path ], [ :get_commands ]], calls
    end
  end

  def test_ignores_broken_command_rpc_when_loading_commands
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      broken_client = Object.new
      broken_client.define_singleton_method(:session_position) do |_append_cursor|
        { known: true, leaf_id: nil, error: nil }
      end
      broken_client.define_singleton_method(:get_commands) do
        calls << [:get_commands]
        raise Errno::EPIPE
      end
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        broken_client
      }]

      response = Rack::MockRequest.new(PiWebGateway).get("/commands", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "Slash commands (6)"
      assert_includes response.body, "/compact"
      assert_includes response.body, "/fork"
      assert_includes response.body, "/tree"
      assert_includes response.body, "/clone"
      assert_includes response.body, "/new"
      assert_includes response.body, "/model"
      assert_equal [[ :start, path ], [ :get_commands ]], calls
    end
  end

  def test_commands_endpoint_rejects_unknown_existing_paths
    Dir.mktmpdir do |dir|
      path = File.join(dir, "not-a-session.jsonl")
      File.write(path, "not a pi session")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).get("/commands", params: { "session" => path })

      assert_equal 404, response.status
      assert_empty calls
    end
  end

  def test_renders_session_status_bar
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "model_change", provider: "openai-codex", modelId: "gpt-5.5" },
        { type: "thinking_level_change", thinkingLevel: "medium" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Hi" }], usage: { totalTokens: 12_345 } } }
      ])
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "session-status-bar"
      assert_includes response.body, "CTX"
      assert_includes response.body, "12.3k"
      assert_includes response.body, "openai-codex/gpt-5.5 (medium)"
      document = Nokogiri::HTML(response.body)
      model_status = document.at_css('button.session-status-item[data-status-key="model"][data-modal-open="model-settings-modal"]')
      refute_nil model_status
      assert_equal "Open model and thinking settings", model_status["aria-label"]
      assert document.at_css('span.session-status-item[data-status-key="ctx"]')
      refute_includes response.body, "Thinking</span>"
    end
  end

  def test_model_status_button_is_disabled_while_session_is_busy
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "model_change", provider: "anthropic", modelId: "claude-sonnet-4" }
      ])
      client = FakeRpcClient.new([])
      def client.busy? = true
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      button = Nokogiri::HTML(response.body).at_css('button[data-status-key="model"]')
      refute_nil button
      assert button.key?("disabled")
    end
  end

  def test_page_includes_accessible_lazy_model_settings_modal
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      modal = document.at_css('body > [data-modal="model-settings-modal"][role="dialog"][aria-modal="true"]')
      refute_nil modal
      assert_equal "model-settings-title", modal["aria-labelledby"]
      assert modal.at_css('input[type="search"][data-model-search]')
      assert modal.at_css('[data-model-list]')
      assert modal.at_css('fieldset[data-thinking-options]')
      assert modal.at_css('button[data-model-settings-apply]')
      assert modal.at_css('button[data-modal-close]')
      assert_includes APP_JAVASCRIPT, 'fetch(`/sessions/model_settings?session=${encodeURIComponent(sessionPath)}`'
      assert_includes APP_JAVASCRIPT, 'fetch("/sessions/model_settings", { method: "POST"'
    end
  end

  def test_model_settings_script_supports_picker_slash_command_and_thinking_shortcut
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function sessionModelSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, 'if (payload?.command === "model")'
      assert_includes APP_JAVASCRIPT, "openModelSettingsModal();"
      assert_includes APP_JAVASCRIPT, "function supportedThinkingLevels(model)"
      assert_includes APP_JAVASCRIPT, "const THINKING_LEVELS = [\"off\", \"minimal\", \"low\", \"medium\", \"high\", \"xhigh\", \"max\"];"
      assert_includes APP_JAVASCRIPT, "function cycleThinkingShortcut(event)"
      assert_includes APP_JAVASCRIPT, '"thinking_level_changed"'
      assert_includes APP_JAVASCRIPT, 'fetch("/sessions/cycle_thinking", { method: "POST"'
      assert_includes APP_JAVASCRIPT, "function handleModelSettingsModalTab(event)"
    end
  end

  def test_returns_session_status_json
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "model_change", provider: "openai-codex", modelId: "gpt-5.5" },
        { type: "thinking_level_change", thinkingLevel: "medium" },
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Hi" }], usage: { totalTokens: 12_345 } } }
      ])
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/status", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal({ "context" => "12.3k", "model" => "openai-codex/gpt-5.5", "thinking" => "medium" }, JSON.parse(response.body))
    end
  end

  def test_returns_estimated_session_status_after_compaction
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "old-entry", timestamp: "2026-06-13T10:00:00Z", message: { role: "assistant", content: [{ type: "text", text: "Old answer" }], usage: { totalTokens: 12_345 } } },
        { type: "message", id: "kept-entry", timestamp: "2026-06-13T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Retained text" }] } },
        { type: "compaction", timestamp: "2026-06-13T10:02:00Z", summary: "Summary text", firstKeptEntryId: "kept-entry" }
      ])
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/status", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal "≈7", JSON.parse(response.body).fetch("context")
    end
  end

  def test_aborts_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/abort",
        params: { "session" => path }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :abort ]], calls
    end
  end

  def test_aborts_selected_session_with_json_response
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/abort",
        params: { "session" => path },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal({ "ok" => true, "session" => path }, JSON.parse(response.body))
      assert_equal [[ :start, path ], [ :abort ]], calls
    end
  end

  def test_abort_does_not_wait_for_a_serialized_session_operation
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      client = FakeRpcClient.new(calls)
      client.define_singleton_method(:busy?) { true }
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      operation_started = Queue.new
      release_operation = Queue.new

      operation_thread = Thread.new do
        registry.with_client(path) do
          operation_started << true
          release_operation.pop
        end
      end
      operation_started.pop
      abort_thread = Thread.new do
        Rack::MockRequest.new(PiWebGateway).post(
          "/abort",
          params: { "session" => path },
          "HTTP_ACCEPT" => "application/json"
        )
      end

      response = Timeout.timeout(1) { abort_thread.value }
      assert_equal 200, response.status
      assert_equal [[:abort]], calls
    ensure
      release_operation << true if operation_thread&.alive?
      operation_thread&.join
      abort_thread&.join
    end
  end

  def test_compacts_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/compact",
        params: { "session" => path, "instructions" => "recent work" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :compact, "recent work" ]], calls
    end
  end

  def test_renames_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/rename",
        params: { "session" => path, "name" => "Useful name" }
      )

      assert_equal 303, response.status
      assert_equal [[ :start, path ], [ :set_session_name, "Useful name" ]], calls
    end
  end

  def test_renders_pending_new_session_before_pi_persists_the_file
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      pending_path = File.join(File.dirname(path), "pending-session.jsonl")
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      client = FakeRpcClient.new([])
      def client.live_snapshot
        {
          event_sequence: 1,
          active_tool_events: [{ "type" => "tool_execution_start", "toolCallId" => "call-1", "toolName" => "subagent", "args" => {} }]
        }
      end
      registry.register(pending_path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => pending_path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "New session (pending first assistant response)"
      assert_includes response.body, pending_path
    end
  end

  def test_sidebar_keeps_active_pending_session_visible_after_switching_away
    Dir.mktmpdir do |dir|
      selected_path = write_session(dir)
      pending_path = File.join(File.dirname(selected_path), "pending-session.jsonl")
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      client = FakeRpcClient.new([], [], pending_path)
      client.define_singleton_method(:busy?) { true }
      registry.register(pending_path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))
      request = Rack::MockRequest.new(PiWebGateway)

      fragment_response = request.get("/session_fragment", params: { "session" => selected_path })

      assert_equal 200, fragment_response.status
      pending_document = Nokogiri::HTML.fragment(JSON.parse(fragment_response.body).fetch("sidebar_html"))
      pending_link = pending_document.at_css("a.session[data-session-path='#{pending_path}']")
      assert pending_link
      assert_equal "New session (pending first assistant response)", pending_link.at_css(".session-title").text
      assert pending_link.at_css(".session-running-indicator")

      File.write(pending_path, [
        JSON.generate({ type: "session", id: "persisted", timestamp: Time.now.utc.iso8601(3), cwd: project_cwd(dir) }),
        JSON.generate({ type: "session_info", name: "Persisted session" })
      ].join("\n") + "\n")

      sidebar_response = request.get("/sidebar", params: { "session" => selected_path })

      assert_equal 200, sidebar_response.status
      persisted_document = Nokogiri::HTML(sidebar_response.body)
      persisted_links = persisted_document.css("a.session[data-session-path='#{pending_path}']")
      assert_equal 1, persisted_links.length
      assert_equal "Persisted session", persisted_links.first.at_css(".session-title").text
    end
  end

  def test_session_view_remaps_active_pending_session_after_pi_persists_the_file
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      File.open(real_path, "a") { |file| file.puts JSON.generate({ type: "session_info", name: "Persisted title" }) }
      pending_path = File.join(File.dirname(real_path), "pending-session.jsonl")
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new([], [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => pending_path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Persisted title"
      assert_includes response.body, real_path
      refute_includes response.body, "New session (pending first assistant response)"
      assert registry.active?(real_path)
      refute registry.active?(pending_path)
    end
  end

  def test_sessions_header_renders_labeled_new_session_action
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })
      document = Nokogiri::HTML(response.body)
      button = document.at_css('.recent-sessions-header [data-modal-open="new-session-modal"]')

      assert_equal 200, response.status
      assert button
      assert_includes button["class"], "sidebar-new-session-button"
      assert_equal "+ New session", button.text.strip.gsub(/\s+/, " ")
      assert_equal "true", button.at_css("span")["aria-hidden"]
      refute document.at_css(".recent-sessions-header [data-sidebar-search-toggle]")
      refute document.at_css('.session-sidebar-header > [data-modal-open="new-session-modal"]')
    end
  end

  def test_renders_discord_like_scrolling_shell
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "app-shell"
      assert_includes response.body, "session-sidebar"
      assert_includes response.body, "conversation-panel"
      assert_includes response.body, "session-header"
      assert_includes response.body, "conversation-scroll"
      assert_includes response.body, "composer"
      assert_includes response.body, "composer-controls"
      assert_includes APP_STYLESHEET, ".composer-state { display: none; align-items: center;"
      refute_includes APP_STYLESHEET, ".composer-state { position: absolute;"
      refute_includes response.body, "Ready"
      assert_includes response.body, "composer-input-row"
      assert_includes response.body, "Attach images"
      assert_includes response.body, "send-button"
      assert_includes response.body, "composer-stop-button"
      assert_includes response.body, "session-abort-button composer-stop-button"
      assert_includes response.body, "Loading…"
      refute_includes response.body, "Loading session…"
      assert_includes APP_JAVASCRIPT, "Send follow-up…"
      assert_includes APP_STYLESHEET, "[hidden] { display: none !important; }"
      assert_includes response.body, "Ask Pi… Enter to send, Shift+Enter for newline."
      refute_includes response.body, "autofocus"
      assert_includes response.body, "Abort running Pi"
      refute_includes response.body, "class=\"danger abort-button session-abort-button\" form=\"abort-form\""
      refute_includes response.body, "Optional compact instructions"
      refute_includes response.body, ">Compact</button>"
      assert_includes APP_JAVASCRIPT, "nearBottom()"
      assert_includes response.body, "jump-controls--top"
      assert_includes response.body, "jump-controls--bottom"
      refute_includes response.body, "jump-controls--message-nav"
      assert_includes response.body, "jump-to-first"
      refute_includes response.body, "message-turn-button"
      assert_includes response.body, "jump-to-latest"
      assert_includes response.body, "aria-label=\"Top\">↑↑</button>"
      assert_includes response.body, "aria-label=\"Bottom\">↓↓</button>"
      refute_includes response.body, "↑↑ top"
      refute_includes response.body, "↓↓ bottom"
      refute_includes response.body, "first ↑"
      refute_includes response.body, "previous ↑"
      refute_includes response.body, "next ↓"
      refute_includes response.body, "latest ↓"
      refute_includes response.body, "lastest ↓"
      assert_includes response.body, "mobile-sidebar-backdrop"
      assert_includes response.body, "aria-label=\"Open sessions\""
      assert_includes response.body, "aria-label=\"Close sessions\""
      assert_includes response.body, "hamburger-icon"
      assert_includes response.body, "session-sidebar-header"
      refute_includes response.body, "mobile-sessions-label\">Sessions"
      assert_includes APP_STYLESHEET, "scrollbar-gutter: stable"
      assert_includes APP_STYLESHEET, ".conversation-scroll { min-height: 0; overflow-y: auto; overflow-x: hidden;"
      assert_includes APP_STYLESHEET, ".jump-controls { position: sticky; z-index: 3; display: flex;"
      assert_includes APP_STYLESHEET, "min-height: 2rem; margin: 0.25rem auto; visibility: hidden; opacity: 0;"
      assert_includes APP_STYLESHEET, ".jump-button { display: none; align-items: center; justify-content: center; width: 2.75rem; height: 2rem; min-height: 0; padding: 0;"
      assert_includes APP_STYLESHEET, ".jump-controls.is-visible { visibility: visible; opacity: 1; }"
      assert_includes APP_STYLESHEET, "body:not(.is-conversation-scrolling) .jump-controls.is-visible { visibility: hidden; opacity: 0; pointer-events: none; }"
      assert_includes APP_JAVASCRIPT, "updateJumpControlsReveal()"
      assert_includes APP_JAVASCRIPT, "this.revealDelayTimer = this.timeout"
      assert_includes APP_JAVASCRIPT, "Date.now() - this.lastRevealAt > 120"
      assert_includes APP_JAVASCRIPT, "}, 300);"
      assert_includes APP_JAVASCRIPT, "this.updateJumpControlsReveal();"
      assert_includes APP_JAVASCRIPT, 'this.scrollDirection === "up" && !this.autoScrollEnabled && !this.nearTop()'
      assert_includes APP_JAVASCRIPT, 'this.scrollDirection === "down" && !this.nearBottom()'
      refute_includes APP_JAVASCRIPT, "expandedMessageAutoFollowPaused"
      refute_includes APP_JAVASCRIPT, "revealExpandedMessageBottom"
      assert_includes APP_JAVASCRIPT, "activateToolOutputRegion(body, { focus: true });"
      assert_includes APP_STYLESHEET, ".message--tool .message-details-summary, .message--tool-transcript .message-details-summary { max-width: 100%; overflow-x: auto; white-space: nowrap; font-family: var(--mono); font-size: 0.84rem; }"
      assert_includes APP_STYLESHEET, ".message--compact .message-details-summary:last-child { margin-bottom: 0; }"
      assert_includes APP_STYLESHEET, ".message--tool .message-body, .message--tool-transcript .message-body { max-width: 100%; overflow-x: auto; }"
      assert_includes APP_STYLESHEET, ".message--tool-transcript .message-body { display: grid; grid-template-columns: minmax(100%, max-content); font-size: 0.84rem; line-height: 1.4; tab-size: 2; white-space: pre; overflow-wrap: normal; word-break: normal; }"
      assert_includes APP_STYLESHEET, ".tool-diff-line { display: block; margin: 0 -0.25rem;"
      assert_includes APP_STYLESHEET, '.tool-output-collapse[data-expanded="true"] [data-tool-output-body] { max-height: min(50dvh, 24rem); overflow-y: auto; scrollbar-gutter: stable; scrollbar-width: thin;'
      assert_includes APP_STYLESHEET, '.tool-output-collapse[data-expanded="true"] [data-tool-output-body] { max-height: min(45dvh, 18rem); }'
      assert_includes APP_STYLESHEET, '.tool-output-collapse[data-expanded="true"] [data-tool-output-body]:focus-visible {'
      assert_includes APP_STYLESHEET, "scrollbar-width: none"
      assert_includes APP_STYLESHEET, ".message--user { margin-left: 10%; background: var(--user-msg); border-color: #ffffff14; color: var(--text); }"
      assert_includes APP_STYLESHEET, ".message--assistant { margin-right: 10%; background: var(--panel); border-color: var(--border); color: var(--copy); }"
      assert_includes APP_STYLESHEET, ".message--thinking { margin-right: 16%; background: transparent; border-color: var(--border-strong); border-style: dashed; color: var(--muted); }"
      assert_includes APP_STYLESHEET, ".message--tool, .message--tool-call { background: var(--tool-ok); border-color: #ffffff0f; color: var(--copy); }"
    end
  end

  def test_selected_session_header_opens_session_only_view_in_new_window
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })
      document = Nokogiri::HTML(response.body)
      link = document.at_css('.session-header-actions a[aria-label="Open session in new window"]')

      assert_equal 200, response.status
      assert link
      assert_equal "_blank", link["target"]
      assert_equal "noopener", link["rel"]
      assert_equal "↗", link.text.strip
      href = URI.parse(link["href"])
      query = Rack::Utils.parse_nested_query(href.query)
      assert_equal "/", href.path
      assert_equal path, query["session"]
      assert_equal "1", query["session_only"]
    end
  end

  def test_session_only_view_renders_conversation_without_sidebar
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path, "session_only" => "1" })
      document = Nokogiri::HTML(response.body)

      assert_equal 200, response.status
      assert document.at_css('body.session-only')
      assert document.at_css(".conversation-panel")
      assert document.at_css(".prompt-form")
      assert document.at_css('.prompt-form input[name="session_only"][value="1"]')
      assert document.at_css('.abort-form input[name="session_only"][value="1"]')
      assert document.at_css('#live-output[data-events-url]')
      refute document.at_css(".session-sidebar")
      refute document.at_css(".mobile-sidebar-backdrop")
      assert_includes APP_JAVASCRIPT, 'if (new URLSearchParams(window.location.search).get("session_only") === "1") formData.set("session_only", "1");'
      assert_includes APP_JAVASCRIPT, "if (!this.element || this.document.hidden || this.modalIsOpen()) return;"
      refute document.at_css('.session-header-actions a[aria-label="Open session in new window"]')
    end
  end

  def test_session_only_fragment_preserves_session_only_url
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/session_fragment", params: { "session" => path, "session_only" => "1" })
      payload = JSON.parse(response.body)
      query = Rack::Utils.parse_nested_query(URI.parse(payload.fetch("url")).query)

      assert_equal 200, response.status
      assert_equal path, query["session"]
      assert_equal "1", query["session_only"]
      assert_nil payload["sidebar_html"]
    end
  end

  def test_renders_recent_sessions_with_project_labels
    Dir.mktmpdir do |dir|
      project_a = File.join(dir, "project-a")
      project_b = File.join(dir, "project-b")
      FileUtils.mkdir_p(project_a)
      FileUtils.mkdir_p(project_b)
      session_dir_a = File.join(dir, "--project-a--")
      session_dir_b = File.join(dir, "--project-b--")
      FileUtils.mkdir_p(session_dir_a)
      FileUtils.mkdir_p(session_dir_b)
      path_a = File.join(session_dir_a, "session-a.jsonl")
      path_b = File.join(session_dir_b, "session-b.jsonl")
      File.write(path_a, [
        JSON.generate({ type: "session", id: "session-a", timestamp: (Time.now - 60).utc.iso8601(3), cwd: project_a }),
        JSON.generate({ type: "session_info", name: "Alpha work" })
      ].join("\n") + "\n")
      File.write(path_b, [
        JSON.generate({ type: "session", id: "session-b", timestamp: Time.now.utc.iso8601(3), cwd: project_b }),
        JSON.generate({ type: "session_info", name: "Beta work" })
      ].join("\n") + "\n")
      FileUtils.touch(path_a, mtime: Time.now - 60)
      FileUtils.touch(path_b, mtime: Time.now)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path_b })

      assert_equal 200, response.status
      assert_includes response.body, "Sessions"
      assert_includes response.body, "recent-sessions"
      assert_includes response.body, "Beta work"
      assert_includes response.body, "Alpha work"
      assert_includes response.body, "project-b"
      assert_includes response.body, "project-a"
      assert_operator response.body.index("Beta work"), :<, response.body.index("Alpha work")
    end
  end

  def test_sidebar_gives_matching_project_names_the_same_visual_identity
    Dir.mktmpdir do |dir|
      projects = [File.join(dir, "machine-a", "pi-web-gateway"), File.join(dir, "machine-b", "pi-web-gateway"), File.join(dir, "machine-b", "acme-platform")]
      projects.each_with_index do |cwd, index|
        FileUtils.mkdir_p(cwd)
        session_dir = File.join(dir, "sessions-#{index}")
        FileUtils.mkdir_p(session_dir)
        File.write(File.join(session_dir, "session-#{index}.jsonl"), [
          JSON.generate({ type: "session", id: "session-#{index}", cwd: cwd }),
          JSON.generate({ type: "session_info", name: "Session #{index}" })
        ].join("\n") + "\n")
      end
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar")
      document = Nokogiri::HTML(response.body)
      identities = document.css(".session-project")
      gateway_identities = identities.select { |identity| identity.at_css(".session-project-label")&.text == "pi-web-gateway" }
      platform_identity = identities.find { |identity| identity.at_css(".session-project-label")&.text == "acme-platform" }

      assert_equal 200, response.status
      assert_equal 2, gateway_identities.length
      assert_equal ["PW"], gateway_identities.map { |identity| identity.at_css(".session-project-monogram").text }.uniq
      assert_equal ["--project-identity-bg: #215f5933; --project-identity-fg: #76cbbf"], gateway_identities.map { |identity| identity["style"] }.uniq
      assert_equal "AP", platform_identity.at_css(".session-project-monogram").text

      filter = document.at_css("[data-project-select] select[data-sidebar-project-filter]")
      gateway_options = filter.css("option").select { |option| option.text == "pi-web-gateway" }
      assert_equal 2, gateway_options.length
      assert_equal ["PW"], gateway_options.map { |option| option["data-project-monogram"] }.uniq
      assert_equal ["#215f5933"], gateway_options.map { |option| option["data-project-background"] }.uniq
      refute filter.at_css('option[value=""]')["data-project-monogram"]
    end
  end

  def test_selected_session_shows_related_parent_and_child_sessions
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      FileUtils.mkdir_p(project_cwd(dir))
      parent_path = File.join(session_dir, "parent.jsonl")
      child_path = File.join(session_dir, "child.jsonl")
      File.write(parent_path, [
        JSON.generate({ type: "session", id: "parent", cwd: project_cwd(dir) }),
        JSON.generate({ type: "session_info", name: "Parent session" })
      ].join("\n") + "\n")
      File.write(child_path, [
        JSON.generate({ type: "session", id: "child", cwd: project_cwd(dir), parentSession: parent_path }),
        JSON.generate({ type: "session_info", name: "Child session" })
      ].join("\n") + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => parent_path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      tree = document.at_css("details.session-relation-tree")
      assert tree
      refute tree["open"]
      assert_includes tree.text, "Related sessions"
      assert_includes tree.text, "Parent session"
      assert_includes tree.text, "Child session"
      assert_equal session_url_for(child_path), tree.at_css('a[href*="child.jsonl"]')["href"]
      assert tree.at_css('.session-relation-tree-node.is-current')
    end
  end

  def test_sidebar_marks_related_sessions_without_rendering_header_fork_clone_buttons
    Dir.mktmpdir do |dir|
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      FileUtils.mkdir_p(project_cwd(dir))
      parent_path = File.join(session_dir, "parent.jsonl")
      child_path = File.join(session_dir, "child.jsonl")
      File.write(parent_path, [
        JSON.generate({ type: "session", id: "parent", cwd: project_cwd(dir) }),
        JSON.generate({ type: "session_info", name: "Parent session" })
      ].join("\n") + "\n")
      File.write(child_path, [
        JSON.generate({ type: "session", id: "child", cwd: project_cwd(dir), parentSession: parent_path }),
        JSON.generate({ type: "session_info", name: "Child session" })
      ].join("\n") + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => child_path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      fork_indicator = document.at_css('.recent-sessions a.session[href*="child.jsonl"] .session-fork-indicator')
      assert_equal "⑂", fork_indicator.text
      assert_equal "Forked from Parent session", fork_indicator["title"]
      refute document.at_css('.recent-sessions a.session[href*="parent.jsonl"] .session-fork-indicator')
      refute document.at_css('.recent-sessions a.session[href*="parent.jsonl"] .session-child-count')
      refute document.at_css('.session-header-actions [data-modal-open="fork-session-modal"]')
      refute document.at_css('.session-header-actions .clone-session-form')
      tree = document.at_css(".session-relation-tree")
      assert_includes tree.text, "Related sessions"
      assert_includes tree.text, "Parent session"
      assert_includes tree.text, "Child session"
    end
  end

  def test_sidebar_search_is_collapsed_until_requested
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      toggle = document.at_css("[data-sidebar-search-toggle]")
      search_form = document.at_css(".sidebar-session-search")
      assert_equal "button", toggle["type"]
      assert_equal "false", toggle["aria-expanded"]
      assert_equal "sidebar-session-search", toggle["aria-controls"]
      assert_equal "sidebar-session-search", search_form["id"]
      assert_equal toggle, document.at_css(".sidebar-project-filter-form .sidebar-filter-row > [data-sidebar-search-toggle]")
      assert_equal search_form, document.at_css(".sidebar-project-filter-form + .sidebar-session-search")
      refute_includes toggle["class"], "is-active"
      refute_includes search_form["class"], "is-open"
    end
  end

  def test_sidebar_search_filters_sessions_by_title_cwd_and_first_user_message
    Dir.mktmpdir do |dir|
      alpha_cwd = File.join(dir, "alpha-project")
      beta_cwd = File.join(dir, "beta-project")
      [alpha_cwd, beta_cwd].each { |cwd| FileUtils.mkdir_p(cwd) }
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      alpha_path = File.join(session_dir, "alpha.jsonl")
      beta_path = File.join(session_dir, "beta.jsonl")
      gamma_path = File.join(session_dir, "gamma.jsonl")
      File.write(alpha_path, [
        JSON.generate({ type: "session", id: "alpha", cwd: alpha_cwd }),
        JSON.generate({ type: "session_info", name: "Alpha refactor" })
      ].join("\n") + "\n")
      File.write(beta_path, [
        JSON.generate({ type: "session", id: "beta", cwd: beta_cwd }),
        JSON.generate({ type: "message", message: { role: "user", content: [{ type: "text", text: "Investigate webhook delivery" }] } })
      ].join("\n") + "\n")
      File.write(gamma_path, [
        JSON.generate({ type: "session", id: "gamma", cwd: alpha_cwd }),
        JSON.generate({ type: "session_info", name: "Gamma cleanup" })
      ].join("\n") + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      title_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => alpha_path, "session_search" => "refactor" })
      message_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => alpha_path, "session_search" => "webhook" })
      cwd_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => alpha_path, "session_search" => "beta-project" })

      assert_equal 200, title_response.status
      assert_includes title_response.body, "Alpha refactor"
      refute_includes title_response.body, "Gamma cleanup"
      refute_includes title_response.body, "No sessions match these filters."
      assert_equal 200, message_response.status
      message_document = Nokogiri::HTML(message_response.body)
      assert_equal ["Current session", "Sessions"], message_document.css(".recent-sessions-header h2").map(&:text)
      assert_equal ["Alpha refactor"], message_document.css(".current-session-section .session-title").map(&:text)
      assert_equal ["Investigate webhook delivery"], message_document.css(".sessions-list .session-title").map(&:text)
      assert_equal 200, cwd_response.status
      cwd_document = Nokogiri::HTML(cwd_response.body)
      assert_equal ["Alpha refactor"], cwd_document.css(".current-session-section .session-title").map(&:text)
      assert_equal ["Investigate webhook delivery"], cwd_document.css(".sessions-list .session-title").map(&:text)
    end
  end

  def test_sidebar_search_renders_empty_state_and_preserves_query_in_links
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 22)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.last, "session_search" => "missing" })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal "missing", document.at_css('.sidebar-session-search input[name="session_search"]')["value"]
      assert_includes document.at_css(".sidebar-session-search")["class"], "is-open"
      assert_equal "true", document.at_css("[data-sidebar-search-toggle]")["aria-expanded"]
      assert_includes response.body, "No sessions match these filters."
      assert_equal ["Session 22"], document.css(".current-session-section .session-title").map(&:text)
      assert_empty document.css(".sessions-list a.session")
      assert_includes document.at_css('.sidebar-project-filter-form input[name="session_search"]')["value"], "missing"
      session_link = document.at_css('.recent-sessions a.session[href]')
      assert_includes session_link["href"], "session_search=missing"
    end
  end

  def test_sidebar_filter_clear_button_resets_search_and_project_together
    Dir.mktmpdir do |dir|
      current_cwd = File.join(dir, "current-project")
      filtered_cwd = File.join(dir, "filtered-project")
      FileUtils.mkdir_p(current_cwd)
      FileUtils.mkdir_p(filtered_cwd)
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      current_path = File.join(session_dir, "current.jsonl")
      filtered_path = File.join(session_dir, "filtered.jsonl")
      File.write(current_path, JSON.generate({ type: "session", id: "current", cwd: current_cwd }) + "\n")
      File.write(filtered_path, JSON.generate({ type: "session", id: "filtered", cwd: filtered_cwd }) + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => current_path, "project" => filtered_cwd, "session_search" => "filtered" }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      toggle = document.at_css("[data-sidebar-search-toggle]")
      clear = document.at_css(".sidebar-filters-clear")
      assert document.at_css(".sidebar-filter-spinner")
      assert_includes toggle["class"], "is-active"
      assert_equal "true", toggle["aria-expanded"]
      assert_equal "Clear filters", clear.text
      assert_equal clear, document.at_css(".sidebar-session-search + .sidebar-filters-clear")
      refute document.at_css(".sidebar-filter-row .sidebar-filters-clear")
      assert_equal "", clear["data-sidebar-filters-clear"]
      assert_includes clear["href"], "session=#{Rack::Utils.escape(current_path)}"
      refute_includes clear["href"], "project="
      refute_includes clear["href"], "session_search="
    end
  end

  def test_session_view_forms_preserve_sidebar_search
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path, "session_search" => "project" })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal "project", document.at_css('.prompt-form input[name="session_search"]')["value"]
      assert_equal "project", document.at_css('.abort-form input[name="session_search"]')["value"]
      assert_equal "project", document.at_css('.new-session-cwd-form input[name="session_search"]')["value"]
    end
  end

  def test_prompt_redirect_preserves_sidebar_search
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new(calls) }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => path, "message" => "hello", "session_search" => "project" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      redirect = JSON.parse(response.body).fetch("redirect")
      assert_includes redirect, "session_search=project"
    end
  end

  def test_new_session_redirect_preserves_sidebar_search
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(dir, "--project--", "new-session.jsonl")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(_cwd) { FakeRpcClient.new([], [], new_path) }]
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new_at_cwd",
        params: { "cwd" => project_cwd(dir), "session" => path, "session_search" => "project" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      redirect = JSON.parse(response.body).fetch("redirect")
      assert_includes redirect, "session_search=project"
    end
  end

  def test_new_session_redirect_does_not_preserve_sidebar_project_filter
    Dir.mktmpdir do |dir|
      production_cwd = File.join(dir, "mixit-production-tool")
      wholesale_cwd = File.join(dir, "mixit-wholesale")
      FileUtils.mkdir_p(production_cwd)
      FileUtils.mkdir_p(wholesale_cwd)
      new_path = File.join(dir, "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :new_rpc_client_factory, [->(started_cwd) {
        calls << [:start_new, started_cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/new_at_cwd",
        params: { "cwd" => wholesale_cwd, "project" => production_cwd },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      redirect = JSON.parse(response.body).fetch("redirect")
      refute_includes redirect, "project="
      assert_equal [[:start_new, File.realpath(wholesale_cwd)], [:get_state]], calls
    end
  end

  def test_sidebar_project_filter_lists_projects_by_recent_session_activity
    Dir.mktmpdir do |dir|
      older_cwd = File.join(dir, "older-project")
      newer_cwd = File.join(dir, "newer-project")
      FileUtils.mkdir_p(older_cwd)
      FileUtils.mkdir_p(newer_cwd)
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      older_path = File.join(session_dir, "older.jsonl")
      newer_path = File.join(session_dir, "newer.jsonl")
      now = Time.now
      File.write(older_path, [
        JSON.generate({ type: "session", id: "older", timestamp: (now - 120).utc.iso8601(3), cwd: older_cwd }),
        JSON.generate({ type: "message", timestamp: (now - 60).utc.iso8601(3), message: { role: "user", content: [{ type: "text", text: "Older prompt" }] } }),
        JSON.generate({ type: "session_info", timestamp: now.utc.iso8601(3), name: "Older work" })
      ].join("\n") + "\n")
      File.write(newer_path, [
        JSON.generate({ type: "session", id: "newer", timestamp: (now - 120).utc.iso8601(3), cwd: newer_cwd }),
        JSON.generate({ type: "message", timestamp: (now - 30).utc.iso8601(3), message: { role: "user", content: [{ type: "text", text: "Newer prompt" }] } }),
        JSON.generate({ type: "session_info", name: "Newer work" })
      ].join("\n") + "\n")
      FileUtils.touch(older_path, mtime: now)
      FileUtils.touch(newer_path, mtime: now - 120)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => newer_path, "project" => older_cwd })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      options = document.css(".sidebar-project-filter option")
      assert_equal ["", newer_cwd, older_cwd], options.map { |option| option["value"] }
      assert_equal ["All projects", "newer-project", "older-project"], options.map(&:text)
      assert_equal older_cwd, options.find { |option| option["selected"] }["value"]
      assert_operator response.body.index("<h2>Sessions</h2>"), :<, response.body.index("sidebar-project-filter")
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal ["Newer work", "Older work"], session_titles
      assert_includes document.at_css('.recent-sessions a.session[href*="older.jsonl"]')["href"], "project=#{Rack::Utils.escape(older_cwd)}"
    end
  end

  def test_sidebar_project_filter_keeps_current_visible_and_filters_unread_sessions
    Dir.mktmpdir do |dir|
      selected_cwd = File.join(dir, "selected-project")
      unread_cwd = File.join(dir, "unread-project")
      filtered_cwd = File.join(dir, "filtered-project")
      [selected_cwd, unread_cwd, filtered_cwd].each { |cwd| FileUtils.mkdir_p(cwd) }
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      selected_path = File.join(session_dir, "selected.jsonl")
      unread_path = File.join(session_dir, "unread.jsonl")
      filtered_path = File.join(session_dir, "filtered.jsonl")
      File.write(selected_path, [
        JSON.generate({ type: "session", id: "selected", cwd: selected_cwd }),
        JSON.generate({ type: "session_info", name: "Selected work" })
      ].join("\n") + "\n")
      File.write(unread_path, [
        JSON.generate({ type: "session", id: "unread", cwd: unread_cwd }),
        JSON.generate({ type: "session_info", name: "Unread work" })
      ].join("\n") + "\n")
      File.write(filtered_path, [
        JSON.generate({ type: "session", id: "filtered", cwd: filtered_cwd }),
        JSON.generate({ type: "session_info", name: "Filtered work" })
      ].join("\n") + "\n")
      FileUtils.touch(selected_path, mtime: Time.now - 30)
      FileUtils.touch(unread_path, mtime: Time.now - 20)
      FileUtils.touch(filtered_path, mtime: Time.now - 10)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => selected_path })
      File.write(unread_path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Unread reply" }] } }) + "\n", mode: "a")

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => selected_path, "project" => filtered_cwd }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal ["Current session", "Sessions"], document.css(".recent-sessions-header h2").map(&:text)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal ["Selected work", "Filtered work"], session_titles
      assert document.at_css('.recent-sessions a.session[href*="selected.jsonl"]')["class"].include?("selected")
      refute document.at_css('.recent-sessions a.session[href*="unread.jsonl"]')
      assert_equal "1", document.at_css(".session-sidebar")["data-unread-session-count"]
    end
  end

  def test_sidebar_project_filter_preserves_project_when_loading_more_sessions
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 41)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths.last, "project" => project_cwd(dir) }
      )

      assert_equal 200, response.status
      load_more = Nokogiri::HTML(response.body).at_css(".sidebar-load-more")
      assert load_more
      assert_includes load_more["href"], "project=#{Rack::Utils.escape(project_cwd(dir))}"
    end
  end

  def test_abort_redirect_preserves_sidebar_project_filter
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/abort",
        params: { "session" => path, "project" => project_cwd(dir) }
      )

      assert_equal 303, response.status
      assert_includes response["Location"], "project=#{Rack::Utils.escape(project_cwd(dir))}"
    end
  end

  def test_sidebar_uses_relative_time_formatter
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal "just now", Nokogiri::HTML(response.body).at_css(".session-meta").text.strip
      refute_includes response.body, "updated"
    end
  end

  def test_sidebar_shows_server_origin_under_heading
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path }, "HTTP_HOST" => "pi.example.test:9292", "rack.url_scheme" => "https")
      document = Nokogiri::HTML(response.body)

      assert_equal 200, response.status
      assert_equal "https://pi.example.test:9292", document.at_css(".sidebar-server-origin").text.strip
      assert_operator response.body.index("<h1>Pi Sessions</h1>"), :<, response.body.index("sidebar-server-origin")
    end
  end

  def test_empty_session_page_prompts_for_existing_directory
    Dir.mktmpdir do |dir|
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/")
      document = Nokogiri::HTML(response.body)

      assert_equal 200, response.status
      assert_equal "Welcome to Pi", document.at_css(".session-header h1").text
      assert_includes document.at_css(".empty-state").text, "Choose an existing project directory"
      empty_state_button = document.at_css('.empty-state [data-modal-open="new-session-modal"]')
      assert_equal "Choose directory", empty_state_button.text.strip
      sidebar_button = document.at_css('.session-sidebar [data-modal-open="new-session-modal"]')
      assert_equal "Choose directory", sidebar_button.text.strip
      refute_includes response.body, "Create a Pi session first, then refresh this page."
    end
  end

  def test_new_session_modal_lists_configured_folder_without_sessions
    Dir.mktmpdir do |dir|
      configured_cwd = File.join(dir, "configured-project")
      missing_cwd = File.join(dir, "missing-project")
      FileUtils.mkdir_p(configured_cwd)
      config_path = File.join(dir, "pinned-dirs")
      File.write(config_path, "#{configured_cwd}\n#{missing_cwd}\n")
      sessions_root = File.join(dir, "sessions")
      FileUtils.mkdir_p(sessions_root)
      PiWebGateway.set :sessions_root, sessions_root
      PiWebGateway.set :session_cwds_path, config_path
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body).at_css('body > [data-modal="new-session-modal"]')
      options = modal.css('select[data-new-session-known-cwd] option')
      assert_equal [configured_cwd], options.map { |option| option["value"] }.reject { |value| value.empty? || value == "__new_path__" }
      assert_equal configured_cwd, modal.at_css('input[name="cwd"]')["value"]
      assert modal.at_css('[data-new-session-project-fields]')
      assert modal.at_css('[data-new-session-path-fields]').key?("hidden")
      refute modal.at_css('button[data-new-session-submit]').key?("disabled")
    end
  end

  def test_new_session_modal_deduplicates_configured_and_session_folders
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      cwd = project_cwd(dir)
      configured_cwd = File.join(dir, "configured-project")
      FileUtils.mkdir_p(configured_cwd)
      config_path = File.join(dir, "pinned-dirs")
      File.write(config_path, "#{cwd}\n#{configured_cwd}\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :session_cwds_path, config_path
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body).at_css('body > [data-modal="new-session-modal"]')
      options = modal.css('select[data-new-session-known-cwd] option')
      assert_equal [cwd, configured_cwd], options.map { |option| option["value"] }.reject { |value| value.empty? || value == "__new_path__" }
    end
  end

  def test_page_includes_new_session_modal
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path, "show_all_sessions" => "1" })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      modal = document.at_css('body > [data-modal="new-session-modal"]')
      assert modal
      assert_equal "/sessions/new_at_cwd", modal.at_css('form.new-session-cwd-form')["action"]
      refute modal.at_css('input[name="show_all_sessions"]')
      refute modal.at_css('input[name="sidebar_sessions_limit"]')
      assert_includes modal.css('option').map { |option| option["value"] }, project_cwd(dir)
      project_option = modal.css('option').find { |option| option["value"] == project_cwd(dir) }
      assert_equal File.basename(project_cwd(dir)), project_option.text
      assert_includes modal.text, "Start session"
      refute_includes modal.text, "Cancel"
      assert modal.at_css('button[data-new-session-submit][data-modal-default-focus]')
      assert_equal "-1", modal.at_css('button[data-modal-close]')["tabindex"]
      assert_includes modal.text, "Project"
      assert modal.at_css('[data-new-session-project-fields]')
      assert modal.at_css('[data-new-session-path-fields]').key?("hidden")
      assert_equal project_cwd(dir), modal.at_css('input[name="cwd"]')["value"]
      assert_equal "__new_path__", modal.at_css('option[data-new-session-new-path-option]')["value"]
    end
  end

  def test_new_session_modal_disambiguates_duplicate_project_names
    Dir.mktmpdir do |dir|
      first_cwd = File.join(dir, "team-a", "app")
      second_cwd = File.join(dir, "team-b", "app")
      [first_cwd, second_cwd].each { |cwd| FileUtils.mkdir_p(cwd) }
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      first_path = File.join(session_dir, "first.jsonl")
      second_path = File.join(session_dir, "second.jsonl")
      File.write(first_path, JSON.generate({ type: "session", id: "first", cwd: first_cwd }) + "\n")
      File.write(second_path, JSON.generate({ type: "session", id: "second", cwd: second_cwd }) + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => first_path })

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body).at_css('body > [data-modal="new-session-modal"]')
      project_options = modal.css('select[data-new-session-known-cwd] option').reject { |option| option["value"].to_s.empty? || option["data-new-session-new-path-option"] }
      assert_equal ["app — #{first_cwd}", "app — #{second_cwd}"], project_options.map(&:text)
      assert_equal ["AP"], project_options.map { |option| option["data-project-monogram"] }.uniq
      assert_equal 1, project_options.map { |option| option["data-project-background"] }.uniq.length
      assert modal.at_css("[data-project-select]")
      refute modal.at_css("option[data-new-session-new-path-option]")["data-project-monogram"]
    end
  end

  def test_new_session_modal_defaults_to_current_session_folder
    Dir.mktmpdir do |dir|
      older_cwd = File.join(dir, "older-project")
      newer_cwd = File.join(dir, "newer-project")
      FileUtils.mkdir_p(older_cwd)
      FileUtils.mkdir_p(newer_cwd)
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      older_path = File.join(session_dir, "older.jsonl")
      newer_path = File.join(session_dir, "newer.jsonl")
      File.write(older_path, JSON.generate({ type: "session", id: "older", timestamp: (Time.now - 60).utc.iso8601(3), cwd: older_cwd }) + "\n")
      File.write(newer_path, JSON.generate({ type: "session", id: "newer", timestamp: Time.now.utc.iso8601(3), cwd: newer_cwd }) + "\n")
      FileUtils.touch(older_path, mtime: Time.now - 60)
      FileUtils.touch(newer_path, mtime: Time.now)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => older_path })

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body).at_css('body > [data-modal="new-session-modal"]')
      options = modal.css('select[data-new-session-known-cwd] option')
      assert_equal [older_cwd, newer_cwd], options.map { |option| option["value"] }.reject { |value| value.empty? || value == "__new_path__" }
      selected_option = options.find { |option| option["selected"] }
      assert_equal older_cwd, selected_option["value"]
      assert_equal older_cwd, modal.at_css('input[name="cwd"]')["value"]
      assert modal.at_css('[data-new-session-path-fields]').key?("hidden")
      assert modal.at_css('[data-new-session-cwd-message]').key?("hidden")
      refute modal.at_css('button[data-new-session-submit]').key?("disabled")
    end
  end

  def test_new_session_modal_defaults_to_filtered_project_folder
    Dir.mktmpdir do |dir|
      current_cwd = File.join(dir, "current-project")
      filtered_cwd = File.join(dir, "filtered-project")
      newer_cwd = File.join(dir, "newer-project")
      [current_cwd, filtered_cwd, newer_cwd].each { |cwd| FileUtils.mkdir_p(cwd) }
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      current_path = File.join(session_dir, "current.jsonl")
      filtered_path = File.join(session_dir, "filtered.jsonl")
      newer_path = File.join(session_dir, "newer.jsonl")
      File.write(current_path, JSON.generate({ type: "session", id: "current", timestamp: (Time.now - 60).utc.iso8601(3), cwd: current_cwd }) + "\n")
      File.write(filtered_path, JSON.generate({ type: "session", id: "filtered", timestamp: (Time.now - 30).utc.iso8601(3), cwd: filtered_cwd }) + "\n")
      File.write(newer_path, JSON.generate({ type: "session", id: "newer", timestamp: Time.now.utc.iso8601(3), cwd: newer_cwd }) + "\n")
      FileUtils.touch(current_path, mtime: Time.now - 60)
      FileUtils.touch(filtered_path, mtime: Time.now - 30)
      FileUtils.touch(newer_path, mtime: Time.now)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => current_path, "project" => filtered_cwd }
      )

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body).at_css('body > [data-modal="new-session-modal"]')
      options = modal.css('select[data-new-session-known-cwd] option')
      assert_equal [filtered_cwd, newer_cwd, current_cwd], options.map { |option| option["value"] }.reject { |value| value.empty? || value == "__new_path__" }
      selected_option = options.find { |option| option["selected"] }
      assert_equal filtered_cwd, selected_option["value"]
      assert_equal filtered_cwd, modal.at_css('input[name="cwd"]')["value"]
    end
  end

  def test_session_fragment_includes_updated_new_session_modal
    Dir.mktmpdir do |dir|
      current_cwd = File.join(dir, "current-project")
      filtered_cwd = File.join(dir, "filtered-project")
      [current_cwd, filtered_cwd].each { |cwd| FileUtils.mkdir_p(cwd) }
      session_dir = File.join(dir, "sessions")
      FileUtils.mkdir_p(session_dir)
      current_path = File.join(session_dir, "current.jsonl")
      filtered_path = File.join(session_dir, "filtered.jsonl")
      File.write(current_path, JSON.generate({ type: "session", id: "current", cwd: current_cwd }) + "\n")
      File.write(filtered_path, JSON.generate({ type: "session", id: "filtered", cwd: filtered_cwd }) + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/session_fragment",
        params: { "session" => current_path, "project" => filtered_cwd }
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      modal = Nokogiri::HTML(payload.fetch("new_session_modal_html"))
      fork_modal = Nokogiri::HTML(payload.fetch("fork_session_modal_html"))
      assert_equal "/sessions/fork_messages?session=#{Rack::Utils.escape(current_path)}", fork_modal.at_css("[data-fork-session-list]")["data-fork-messages-url"]
      selected_option = modal.at_css('select[data-new-session-known-cwd] option[selected]')
      assert_equal filtered_cwd, selected_option["value"]
      assert_equal filtered_cwd, modal.at_css('input[name="cwd"]')["value"]
    end
  end

  def test_new_session_modal_fragment_defaults_to_filtered_project_folder
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      filtered_cwd = File.join(dir, "filtered-project")
      FileUtils.mkdir_p(filtered_cwd)
      filtered_path = File.join(dir, "filtered.jsonl")
      File.write(filtered_path, JSON.generate({ type: "session", id: "filtered", cwd: filtered_cwd }) + "\n")
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/new_session_modal",
        params: { "session" => path, "project" => filtered_cwd }
      )

      assert_equal 200, response.status
      modal = Nokogiri::HTML(response.body)
      selected_option = modal.at_css('select[data-new-session-known-cwd] option[selected]')
      assert_equal filtered_cwd, selected_option["value"]
      assert_equal filtered_cwd, modal.at_css('input[name="cwd"]')["value"]
    end
  end

  def test_page_includes_generic_modal_and_new_session_cwd_scripts
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function openModal(modal)"
      assert_includes APP_JAVASCRIPT, "function closeModal(modal)"
      assert_includes APP_JAVASCRIPT, "function modalIsOpen()"
      assert_includes APP_JAVASCRIPT, "function focusPromptAfterModalClose(modal)"
      assert_includes APP_JAVASCRIPT, 'if (opener.dataset.modalOpen === "new-session-modal") {'
      assert_includes APP_JAVASCRIPT, "export class ProjectSelectController"
      assert_includes APP_JAVASCRIPT, "export class NewSessionFormController"
      assert_includes APP_JAVASCRIPT, "sidebarController.initialize();"
      assert_includes APP_JAVASCRIPT, "newSessionFormController.initialize();"
      assert_includes APP_STYLESHEET, ".session-switch-overlay { position: fixed; inset: 0; z-index: 140;"
      assert_includes APP_STYLESHEET, ".modal-overlay { place-items: end stretch; padding: 0; }"
      assert_includes APP_JAVASCRIPT, "submit.textContent = \"Starting…\""
      assert_includes APP_JAVASCRIPT, "function replaceNewSessionModalHtml(html)"
      assert_includes APP_JAVASCRIPT, "const [sidebarResponse, modalResponse] = await Promise.all(["
      assert_includes APP_JAVASCRIPT, "fetch(newSessionModalUrl(targetUrl.href))"
      assert_includes APP_JAVASCRIPT, "replaceNewSessionModalHtml(event.detail.modalHtml);"
      assert_includes APP_JAVASCRIPT, "replaceNewSessionModalHtml(payload.new_session_modal_html);"
      assert_includes APP_JAVASCRIPT, "replaceForkSessionModalHtml(payload.fork_session_modal_html);"
      refute_includes response.body, "data-modal-open=\"fork-session-modal\""
      refute_includes response.body, "class=\"clone-session-form\""
      assert_includes APP_JAVASCRIPT, "function loadForkMessages(modal)"
      assert_includes APP_JAVASCRIPT, "fetch(\"/sessions/fork\", { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } })"
      assert_includes APP_JAVASCRIPT, "await switchToBranchedSession(payload);"
      assert_includes APP_JAVASCRIPT, "const originalForkText = forkOption.textContent;"
      assert_includes APP_JAVASCRIPT, "forkOption.textContent = originalForkText;"
      assert_includes APP_JAVASCRIPT, "showStatus(\"Could not fork this session\", true);"
      assert_includes APP_JAVASCRIPT, "modal.querySelector(\"[data-modal-default-focus]:not(:disabled)\")"
      assert_includes APP_JAVASCRIPT, "focusPromptAfterModalClose(modal);"
      refute_includes APP_JAVASCRIPT, "function makeForkButton(entryId)"
      refute_includes APP_JAVASCRIPT, "function forkEntryIdFromEvent(event, message)"
      refute_includes APP_JAVASCRIPT, "function scheduleResolveForkButton(entry, text)"
      assert_includes APP_JAVASCRIPT, "abortEventPoll();"
      assert_includes APP_JAVASCRIPT, "async function submitAbort(event)"
      assert_includes APP_JAVASCRIPT, "if (modalIsOpen()) return;"
      modal = Nokogiri::HTML(response.body).at_css('[data-modal="new-session-modal"]')
      cwd_form = modal.at_css("form.new-session-cwd-form")
      cwd_input = modal.at_css("[data-new-session-cwd-input]")
      assert_equal "/sessions/browse_cwd", cwd_form["data-cwd-browser-url"]
      assert_equal "combobox", cwd_input["role"]
      assert_equal "list", cwd_input["aria-autocomplete"]
      assert_equal "new-session-cwd-suggestions", cwd_input["aria-controls"]
      assert_equal "listbox", modal.at_css("#new-session-cwd-suggestions")["role"]
      assert_includes APP_JAVASCRIPT, "fetch(browserUrl"
      assert_includes APP_JAVASCRIPT, "renderSuggestions(form, directories)"
      assert_includes APP_JAVASCRIPT, "selectSuggestion(form, path)"
      assert_includes APP_JAVASCRIPT, "handleKeydown(event, form)"
      assert_includes APP_JAVASCRIPT, "focusout: (event) => this.handleFocusout(event, form)"
      assert_includes APP_JAVASCRIPT, 'event.key === "Escape" && modalIsOpen() && !event.defaultPrevented'
      refute_includes APP_JAVASCRIPT, "input.value = resolvedCwd"
      assert_includes APP_JAVASCRIPT, "setProjectMode(form)"
      assert_includes APP_JAVASCRIPT, "setPathMode(form,"
      assert_includes APP_JAVASCRIPT, "newSessionFormController.sync(form)"
      assert_includes response.body, "data-new-session-new-path-option"
      assert_includes APP_JAVASCRIPT, "hasAttribute(\"data-new-session-new-path-option\")"
      assert_includes APP_JAVASCRIPT, "form.dataset.submitting === \"true\""
      assert_includes APP_JAVASCRIPT, "function addSessionViewFormParams(formData)"
      assert_includes APP_JAVASCRIPT, "form.action, { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } }"
      assert_includes APP_JAVASCRIPT, "const sessionSearch = sidebarController.activeSearch();"
      assert_includes APP_JAVASCRIPT, "if (sessionSearch) formData.set(\"session_search\", sessionSearch);"
      refute_includes APP_JAVASCRIPT, "showAllSessionsActive"
      refute_includes APP_JAVASCRIPT, "activeSidebarSessionsLimit"
      assert_includes APP_JAVASCRIPT, "const currentProject = new URLSearchParams(location.search).get(\"project\");"
      assert_includes APP_JAVASCRIPT, "if (currentProject) url.searchParams.set(\"project\", currentProject);"
      assert_includes APP_JAVASCRIPT, "this.temporarySessionsLimit = null;"
      assert_includes APP_JAVASCRIPT, "target.searchParams.set(\"sidebar_sessions_limit\", this.temporarySessionsLimit);"
      assert_includes APP_JAVASCRIPT, "this.temporarySessionsLimit = targetUrl.searchParams.get(\"sidebar_sessions_limit\") || this.temporarySessionsLimit;"
      refute_includes APP_JAVASCRIPT, "history.replaceState(history.state"
      assert_includes APP_JAVASCRIPT, "controlsActive()"
      assert_includes APP_JAVASCRIPT, "if (this.controlsActive() || this.recentlyInteracted())"
      assert_includes APP_JAVASCRIPT, "async changeProjectFilter(select)"
      assert_includes APP_JAVASCRIPT, "this.replace(html, { scrollTop: 0, notify: false });"
    end
  end

  def test_recent_sessions_include_keyboard_shortcut_indices
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 9)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths.last }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      selected = document.at_css(".recent-sessions a.session.selected")
      assert_equal "1", selected["data-session-shortcut"]
      assert_equal "1", selected.at_css(".session-shortcut").text
      shortcuts = document.css(".recent-sessions a.session").map { |link| [link["data-session-shortcut"], link.at_css(".session-shortcut")&.text] }
      assert_equal (1..9).map { |number| [number.to_s, number.to_s] }, shortcuts
    end
  end

  def test_sidebar_numbers_current_session_in_chronological_order
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 3)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths[1] }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      links = document.css(".recent-sessions a.session")
      assert_equal ["Session 3", "Session 2", "Session 1"], links.map { |link| link.at_css(".session-title").text }
      assert links[1]["class"].include?("selected")
      assert_equal ["1", "2", "3"], links.map { |link| link["data-session-shortcut"] }
      assert_equal "Sessions", document.css(".recent-sessions-header h2").map(&:text).first
      assert_operator response.body.index("<h2>Sessions</h2>"), :<, response.body.index("Session 1")
    end
  end

  def test_sidebar_keeps_unread_sessions_in_chronological_order
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 4)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.first })
      now = Time.now
      File.write(paths[3], JSON.generate({ type: "message", timestamp: (now - 10).utc.iso8601(3), message: { role: "user", content: [{ type: "text", text: "Newest" }] } }) + "\n", mode: "a")
      File.write(paths[1], JSON.generate({ type: "message", timestamp: (now - 20).utc.iso8601(3), message: { role: "assistant", content: [{ type: "text", text: "Unread done" }] } }) + "\n", mode: "a")
      File.write(paths[2], JSON.generate({ type: "message", timestamp: (now - 30).utc.iso8601(3), message: { role: "user", content: [{ type: "text", text: "Older" }] } }) + "\n", mode: "a")
      File.write(paths.first, JSON.generate({ type: "message", timestamp: (now - 40).utc.iso8601(3), message: { role: "assistant", content: [{ type: "text", text: "Current done" }] } }) + "\n", mode: "a")

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.first })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal ["Sessions"], document.css(".recent-sessions-header h2").map(&:text)
      links = document.css(".recent-sessions a.session")
      assert_equal ["Session 4", "Session 2", "Session 3", "Session 1"], links.map { |link| link.at_css(".session-title").text }
      assert_equal ["1", "2", "3", "4"], links.map { |link| link["data-session-shortcut"] }
      assert links[1]["class"].include?("unread")
      refute links.last["class"].include?("unread")
    end
  end

  def test_mobile_hamburger_shows_unread_session_count
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 4)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.first })
      File.write(paths[1], JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Unread two" }] } }) + "\n", mode: "a")
      File.write(paths[2], JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Unread three" }] } }) + "\n", mode: "a")

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => paths.first })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      badges = document.css(".mobile-sessions-unread-badge")
      assert_equal ["2", "2"], badges.map(&:text)
      assert badges.all? { |badge| badge["aria-label"] == "2 unread sessions" }
      assert_includes APP_STYLESHEET, ".mobile-sessions-unread-badge"
    end
  end

  def test_mobile_hamburger_hides_unread_session_count_when_none
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => paths.last })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_empty document.css(".mobile-sessions-unread-badge")
    end
  end

  def test_sidebar_uses_one_sessions_header
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.last })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal ["Sessions"], document.css(".recent-sessions-header h2").map(&:text)
    end
  end

  def test_sidebar_uses_one_flat_sessions_list_without_project_groups
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 11)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths.last }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal ["Session 11", "Session 10", "Session 9", "Session 8", "Session 7", "Session 6", "Session 5", "Session 4", "Session 3", "Session 2", "Session 1"], session_titles
      assert_empty document.css(".cwd-group")
    end
  end

  def test_trims_sidebar_sessions_to_latest_twenty_by_default
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 41)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal 20, session_titles.length
      assert_equal "Session 41", session_titles.first
      assert_equal "Session 22", session_titles.last
      refute_includes response.body, "Session 20"
      load_more = document.at_css(".sidebar-load-more")
      assert load_more
      assert_equal "Load 20 more", load_more.text.gsub(/\s+/, " ").strip
      assert_includes load_more["href"], "sidebar_sessions_limit=40"
      assert load_more.at_css(".sidebar-load-more-spinner")
      assert_empty document.css(".cwd-group")
    end
  end

  def test_moves_older_current_session_from_separate_section_into_loaded_page
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 42)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]
      request = Rack::MockRequest.new(PiWebGateway)

      response = request.get("/", params: { "session" => paths.first })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal ["Current session", "Sessions"], document.css(".recent-sessions-header h2").map(&:text)
      assert_equal ["Session 1"], document.css(".current-session-section a.session .session-title").map(&:text)
      assert_equal (23..42).to_a.reverse.map { |index| "Session #{index}" }, document.css(".sessions-list a.session .session-title").map(&:text)
      assert_equal "1", document.at_css(".current-session-section a.session.selected")["data-session-shortcut"]
      assert_equal (2..9).map(&:to_s), document.css(".sessions-list a.session").first(8).map { |link| link["data-session-shortcut"] }
      assert_nil document.css(".sessions-list a.session")[8]["data-session-shortcut"]

      loaded_response = request.get("/", params: { "session" => paths.first, "sidebar_sessions_limit" => "42" })

      assert_equal 200, loaded_response.status
      loaded_document = Nokogiri::HTML(loaded_response.body)
      assert_equal ["Sessions"], loaded_document.css(".recent-sessions-header h2").map(&:text)
      assert_empty loaded_document.css(".current-session-section")
      loaded_links = loaded_document.css(".sessions-list a.session")
      assert_equal 42, loaded_links.length
      assert_equal "Session 1", loaded_links.last.at_css(".session-title").text
      assert loaded_links.last["class"].include?("selected")
      assert_nil loaded_links.last["data-session-shortcut"]
      refute loaded_document.at_css(".sidebar-load-more")
    end
  end

  def test_loads_next_sidebar_session_page
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 41)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last, "sidebar_sessions_limit" => "40" }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal 40, session_titles.length
      assert_equal "Session 41", session_titles.first
      assert_equal "Session 2", session_titles.last
      load_more = document.at_css(".sidebar-load-more")
      assert_equal "Load 1 more", load_more.text.gsub(/\s+/, " ").strip
      assert_includes load_more["href"], "sidebar_sessions_limit=41"
      session_link = document.at_css('.recent-sessions a.session[href*="session-2.jsonl"]')
      refute_includes session_link["href"], "sidebar_sessions_limit"
      refute_includes session_link["href"], "show_all_sessions"
    end
  end

  def test_returns_sidebar_fragment_for_selected_session
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths.first, "expanded_cwd" => [project_cwd(dir)] }
      )

      assert_equal 200, response.status
      assert_includes response.body, "session-sidebar"
      refute_includes response.body, "expanded_cwd"
      assert_includes response.body, "selected"
      assert_includes response.body, "Session 1"
    end
  end

  def test_initial_session_render_exposes_latest_message_window
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, (1..180).map { |index| { role: "user", text: "Message #{index}" } })
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      conversation = document.at_css("#conversation-scroll")
      assert_equal "true", conversation["data-has-older-messages"]
      assert_equal "30", conversation["data-older-message-count"]
      text = conversation.text
      refute_includes text, "Message 30"
      assert_includes text, "Message 31"
      assert_includes text, "Message 180"
    end
  end

  def test_session_fragment_exposes_latest_message_window
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, (1..180).map { |index| { role: "user", text: "Message #{index}" } })
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/session_fragment", params: { "session" => path })

      assert_equal 200, response.status
      html = JSON.parse(response.body).fetch("conversation_html")
      document = Nokogiri::HTML(html)
      conversation = document.at_css("#conversation-scroll")
      assert_equal "true", conversation["data-has-older-messages"]
      assert_equal "30", conversation["data-older-message-count"]
      text = conversation.text
      refute_includes text, "Message 30"
      assert_includes text, "Message 31"
      assert_includes text, "Message 180"
    end
  end

  def test_returns_session_fragment_for_selected_session
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/session_fragment",
        params: { "session" => paths.first, "expanded_cwd" => [project_cwd(dir)] }
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      payload = JSON.parse(response.body)
      assert_equal paths.first, payload.fetch("session")
      assert_equal "Session 1", payload.fetch("title")
      assert_includes payload.fetch("url"), Rack::Utils.escape(paths.first)
      refute_includes payload.fetch("url"), "expanded_cwd"
      assert_includes payload.fetch("sidebar_html"), "session-sidebar"
      refute_includes payload.fetch("sidebar_html"), "expanded_cwd"
      assert_includes payload.fetch("sidebar_html"), "selected"
      assert_includes payload.fetch("conversation_html"), "conversation-panel"
      refute_includes payload.fetch("conversation_html"), "expanded_cwd"
      assert_includes payload.fetch("conversation_html"), paths.first
      assert_includes payload.fetch("conversation_html"), "project"
      assert_includes payload.fetch("conversation_html"), "session-header-project"
    end
  end

  def test_does_not_render_fork_button_for_user_messages
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", id: "user-entry-1", message: { role: "user", content: [{ type: "text", text: "Fork me" }] } },
        { type: "message", id: "assistant-entry-1", message: { role: "assistant", content: [{ type: "text", text: "No fork button" }] } }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      user_message = document.at_css('[data-role="user"]')
      assistant_message = document.at_css('[data-role="assistant"]')
      assert_nil user_message.at_css("[data-fork-entry-id]")
      assert_nil assistant_message.at_css("[data-fork-entry-id]")
    end
  end

  def test_renders_selected_session_header_with_project_label
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      header = document.at_css(".session-header")
      project = header.at_css(".session-header-project")
      assert_equal "project", project.at_css(".session-header-project-label").text
      assert_equal "PR", project.at_css(".session-header-project-icon").text
      assert_equal "project", project["title"]
      stop_button = header.at_css('button.composer-stop-button[form="abort-form"]')
      refute_nil stop_button
      assert_equal "Abort running Pi", stop_button["aria-label"]
      assert stop_button.key?("hidden")
    end
  end

  def test_renders_compaction_entries_as_compact_status_messages
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "compaction", timestamp: "2026-06-13T10:00:00Z", summary: "Important summary" }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, 'class="message message--status message--compact" data-role="status"'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary">Conversation compacted</span></div>'
      assert_includes response.body, "Important summary"
    end
  end

  def test_renders_messages_with_role_specific_structure
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [
        { role: "user", text: "Hello <Pi>" },
        { role: "assistant", text: "Hi there" },
        { role: "system", text: "System note" },
        { role: "custom", text: "Session renamed" },
        { role: "toolResult", text: "Tool output" },
        { role: "error", text: "Something failed" }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'class="message message--user" data-role="user"'
      assert_includes response.body, 'class="message message--assistant" data-role="assistant"'
      assert_includes response.body, 'data-final-assistant-response="true"'
      assert_includes response.body, 'class="message message--status" data-role="system"'
      assert_includes response.body, 'class="message message--status" data-role="custom"'
      assert_includes response.body, 'message--tool'
      assert_includes response.body, 'data-role="toolResult"'
      assert_includes response.body, 'class="message message--error" data-role="error"'
      assert_includes response.body, 'class="message-body"'
      assert_includes response.body, "Hello &lt;Pi&gt;"
      assert_includes response.body, Time.parse("2026-06-13T10:00:00Z").localtime.strftime("%Y-%m-%d %H:%M")
      refute_includes response.body, "Hello <Pi>"
      assert_includes APP_JAVASCRIPT, "messageRoleKey"
    end
  end

  def test_marks_only_final_assistant_text_responses_for_navigation
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          message: {
            role: "assistant",
            content: [{ type: "thinking", thinking: "not final" }]
          }
        },
        {
          type: "message",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", name: "bash", arguments: { command: "echo hi" } }]
          }
        },
        {
          type: "message",
          message: {
            role: "assistant",
            content: [{ type: "text", text: "Final answer" }]
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal 1, document.css('[data-final-assistant-response="true"]').length
      assert_equal "Final answer", document.at_css('[data-final-assistant-response="true"] .message-body').text.strip
    end
  end

  def test_message_navigation_arrows_are_not_rendered
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      refute_includes response.body, "message-turn-button"
      refute_includes response.body, "message-turn-nav"
      refute_includes response.body, "previousFinalAssistantResponse"
      refute_includes response.body, "nextFinalAssistantResponse"
      refute_includes APP_JAVASCRIPT, 'const turnButton = event.target.closest(".message-turn-button");'
    end
  end

  def test_renders_mixed_assistant_thinking_separately_from_markdown_answer
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "**Heading**\n\nPrivate **reasoning**" },
              { type: "text", text: "## Visible answer" }
            ]
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'message--thinking'
      refute_includes response.body, 'Thinking:'
      assert_includes response.body, 'Private <strong>reasoning</strong>'
      refute_includes response.body, '<div class="message-details-summary"><span class="compact-summary">thinking</span></div>'
      refute_includes response.body, "**Heading**"
      refute_includes response.body, "**reasoning**"
      assert_includes response.body, 'class="message-body message-body--thinking message-body--markdown"'
      assert_includes response.body, 'class="message-body message-body--markdown"'
      assert_includes response.body, "<h2>Visible answer</h2>"
    end
  end

  def test_renders_assistant_markdown_and_sanitizes_html
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [
        { role: "assistant", text: "## Plan\n\n- One\n- `two`\n\n```ruby\nputs :ok\n```\n\n<script>alert('x')</script><a href=\"javascript:alert(1)\">bad</a>" },
        { role: "user", text: "## Not markdown <script>alert('user')</script>" }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'class="message-body message-body--markdown"'
      assert_includes response.body, "<h2>Plan</h2>"
      assert_includes response.body, "<li>One</li>"
      assert_includes response.body, "<code>two</code>"
      assert_includes response.body, "<pre><code class=\"highlight ruby\"><span class=\"syntax-function\">puts</span> <span class=\"syntax-symbol\">:ok</span>\n</code></pre>"
      refute_includes response.body, "<script>alert"
      refute_includes response.body, "javascript:alert"
      assert_includes response.body, "## Not markdown &lt;script&gt;alert(&#39;user&#39;)&lt;/script&gt;"
    end
  end

  def test_renders_tools_as_compact_details_and_thinking_inline
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "Thinking through the problem" },
              { type: "toolCall", name: "bash", arguments: { command: "ls" } }
            ],
            stopReason: "toolUse"
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "toolResult",
            toolName: "bash",
            content: [{ type: "text", text: "file list" }],
            isError: false
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "toolResult",
            toolName: "edit",
            content: [{ type: "text", text: "No match" }],
            isError: true
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, 'class="message message--assistant message--thinking" data-role="assistant"'
      refute_includes response.body, 'Thinking:'
      assert_includes response.body, 'Thinking through the problem'
      refute_includes response.body, '<div class="message-details-summary"><span class="compact-summary">thinking</span></div>'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary">$ ls</span></div>'
      assert_includes response.body, 'class="message message--tool message--compact" data-role="toolResult"'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary">bash</span></div>'
      assert_includes response.body, 'class="message message--tool message--compact message--tool-transcript message--tool-error" data-role="toolResult"'
      refute_includes response.body, '<details class="message-details"'
      assert_includes response.body, "Thinking through the problem"
      assert_includes response.body, "file list"
    end
  end

  def test_open_session_renders_images_from_read_results
    Dir.mktmpdir do |dir|
      image_data = Base64.strict_encode64("fake image data")
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "read-1", name: "read", arguments: { path: "/tmp/screenshot.png" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "read-1",
            toolName: "read",
            content: [
              { type: "text", text: "Read image file [image/png]" },
              { type: "image", data: image_data, mimeType: "image/png" }
            ],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      tool_card = compact_card_with_summary(document, "read /tmp/screenshot.png")
      image = tool_card&.at_css(".message-images .message-image")
      assert image
      assert_equal "data:image/png;base64,#{image_data}", image["src"]
      assert_equal "lazy", image["loading"]
      assert_equal "async", image["decoding"]
    end
  end

  def test_open_session_shortens_home_paths_in_tool_transcript_display
    Dir.mktmpdir do |dir|
      home_path = File.join(Dir.home, "Work", ".worktrees", "demo", "feature_branch")
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "bash-1", name: "bash", arguments: { command: "cd #{home_path} && cd #{Dir.home} && rg shipped app", timeout: 30 } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "bash-1",
            toolName: "bash",
            content: [{ type: "text", text: "path=#{home_path}/app/models/order.rb \"#{home_path}/app/services/check.rb\": shipped home=#{Dir.home}:" }],
            isError: false
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "read-1", name: "read", arguments: { path: File.join(home_path, "app/models/order.rb"), offset: 1, limit: 2 } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "toolResult",
            toolCallId: "edit-1",
            toolName: "edit",
            content: [{ type: "text", text: "Updated" }],
            details: { diff: "+ path=#{home_path}/config/routes.rb" },
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "$ cd ~/Work/.worktrees/demo/feature_branch &amp;&amp; cd ~ &amp;&amp; rg shipped app (timeout 30s)"
      assert_includes response.body, "path=~/Work/.worktrees/demo/feature_branch/app/models/order.rb"
      assert_includes response.body, "&quot;~/Work/.worktrees/demo/feature_branch/app/services/check.rb&quot;: shipped home=~:"
      assert_includes response.body, '<span class="tool-path">~/Work/.worktrees/demo/feature_branch/app/models/order.rb</span>'
      assert_includes response.body, "+ path=~/Work/.worktrees/demo/feature_branch/config/routes.rb"
      refute_includes response.body, home_path
    end
  end

  def test_collapses_long_tool_outputs_to_latest_lines
    Dir.mktmpdir do |dir|
      long_output = (1..25).map { |index| "line #{index}" }.join("\n")
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "bash-1", name: "bash", arguments: { command: "long command" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "bash-1",
            toolName: "bash",
            content: [{ type: "text", text: long_output }],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      tool_card = compact_card_with_summary(document, "$ long command")
      assert tool_card
      assert_equal "true", tool_card.at_css("[data-tool-output-collapse]")["data-collapsed"]
      assert_includes tool_card.at_css("[data-tool-output-collapse-control]").text, "earlier lines"
      assert_equal "Expand", tool_card.at_css("[data-tool-output-toggle]").text
      visible_lines = tool_card.at_css(".message-body").css(".tool-output-line").map(&:text)
      refute_includes visible_lines, "line 1"
      assert_includes visible_lines, "line 8"
      assert_includes visible_lines, "line 25"
    end
  end

  def test_does_not_show_expand_when_all_tool_output_lines_are_visible_on_desktop
    Dir.mktmpdir do |dir|
      output = (1..18).map { |index| "line #{index}" }.join("\n")
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "bash-1", name: "bash", arguments: { command: "medium command" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "bash-1",
            toolName: "bash",
            content: [{ type: "text", text: output }],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      tool_card = compact_card_with_summary(document, "$ medium command")
      assert tool_card
      assert_nil tool_card.at_css("[data-tool-output-collapse]")
      visible_lines = tool_card.css(".message-body .tool-output-line").map(&:text)
      assert_equal "line 1", visible_lines.first
      assert_equal "line 18", visible_lines.last
    end
  end

  def test_does_not_tail_collapse_final_assistant_or_thinking_messages
    Dir.mktmpdir do |dir|
      long_text = (1..25).map { |index| "assistant line #{index}" }.join("\n")
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: { role: "assistant", content: [{ type: "thinking", thinking: long_text }] }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: { role: "assistant", content: [{ type: "text", text: long_text }] }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_empty document.css("[data-tool-output-collapse]")
      assert_includes response.body, "assistant line 1"
      assert_includes response.body, "assistant line 25"
    end
  end

  def test_renders_read_edit_and_write_tools_as_collapsed_transcripts
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "read-1", name: "read", arguments: { path: "test/app_test.rb", offset: 545, limit: 110 } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "read-1",
            toolName: "read",
            content: [{ type: "text", text: "545 assert_equal 200, response.status\n546 assert_includes response.body, 'message--thinking'" }],
            isError: false
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "edit-1", name: "edit", arguments: { path: "test/pi_session_store_test.rb", edits: [{ oldText: "old", newText: "new" }] } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:01Z",
          message: {
            role: "toolResult",
            toolCallId: "edit-1",
            toolName: "edit",
            content: [{ type: "text", text: "Successfully replaced 1 block(s) in test/pi_session_store_test.rb." }],
            details: { diff: " 70 assert_equal [false, false], messages.map(&:compact)\n+71 assert_equal [true, false], messages.map(&:thinking)" },
            isError: false
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "write-1", name: "write", arguments: { path: "notes/status.txt", content: "done\n<script>alert('x')</script>" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:01Z",
          message: {
            role: "toolResult",
            toolCallId: "write-1",
            toolName: "write",
            content: [{ type: "text", text: "Wrote notes/status.txt" }],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_empty document.css("[data-tool-output-collapse]")
      assert_includes response.body, 'message--tool-transcript'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary"><span class="tool-command">read</span> <span class="tool-path">test/app_test.rb</span><span class="tool-range">:545-654</span></span></div>'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary"><span class="tool-command">edit</span> <span class="tool-path">test/pi_session_store_test.rb</span></span></div>'
      assert_includes response.body, '<div class="message-details-summary"><span class="compact-summary"><span class="tool-command">write</span> <span class="tool-path">notes/status.txt</span></span></div>'
      refute_includes response.body, 'details-collapse-button'
      refute_includes response.body, '<details class="message-details"'
      refute_includes response.body, 'message-body message-body--edit-preview'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--add">+71 assert_equal [true, false], messages.map(&amp;:thinking)</span>'
      refute_includes response.body, '545 assert_equal 200, response.status'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--add">+ done</span>'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--add">+ &lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt;</span>'
      refute_includes response.body, '<script>alert'
      assert_includes response.body, 'Wrote notes/status.txt'
    end
  end

  def test_failed_edit_tool_results_keep_preview_styling
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "edit-1", name: "edit", arguments: { path: "app.rb", edits: [{ oldText: "old", newText: "new" }] } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "edit-1",
            toolName: "edit",
            content: [{ type: "text", text: "Edit failed" }],
            isError: true
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, 'message-body message-body--edit-preview'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--meta tool-diff-line--preview-heading">Edit 1</span>'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--remove">- old</span>'
      assert_includes response.body, 'Edit failed'
    end
  end

  def test_renders_unpaired_edit_tool_results_with_diff_coloring
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "toolResult",
            toolName: "edit",
            content: [{ type: "text", text: "edited" }],
            details: { diff: "- old\n+ new" },
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, 'message--tool-transcript'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--remove">- old</span>'
      assert_includes response.body, '<span class="tool-diff-line tool-diff-line--add">+ new</span>'
    end
  end

  def test_renders_failed_tool_transcripts_collapsed_with_call_context
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "edit-fail", name: "edit", arguments: { path: "TODO.md", edits: [{ oldText: "old item", newText: "new item" }] } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:00:01Z",
          message: {
            role: "toolResult",
            toolCallId: "edit-fail",
            toolName: "edit",
            content: [{ type: "text", text: "oldText did not match" }],
            isError: true
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "read-fail", name: "read", arguments: { path: "missing.txt" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:01Z",
          message: {
            role: "toolResult",
            toolCallId: "read-fail",
            toolName: "read",
            content: [{ type: "text", text: "No such file" }],
            isError: true
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "write-fail", name: "write", arguments: { path: "readonly.txt", content: "new contents" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:02:01Z",
          message: {
            role: "toolResult",
            toolCallId: "write-fail",
            toolName: "write",
            content: [{ type: "text", text: "Permission denied" }],
            isError: true
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:03:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "bash-fail", name: "bash", arguments: { command: "false" } }]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:03:01Z",
          message: {
            role: "toolResult",
            toolCallId: "bash-fail",
            toolName: "bash",
            content: [{ type: "text", text: "Command exited with code 1" }],
            isError: true
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      refute_includes response.body, '<details class="message-details"'
      assert_includes response.body, 'class="message message--assistant message--compact message--tool-call message--tool-transcript message--tool-error" data-role="assistant"'
      document = Nokogiri::HTML(response.body)
      page_text = document.text

      assert_includes response.body, 'Edit 1'
      assert_includes response.body, '- old item'
      assert_includes response.body, '+ new item'
      assert_includes response.body, 'oldText did not match'
      assert_includes page_text, 'read missing.txt'
      assert_includes response.body, 'No such file'
      assert_includes page_text, 'write readonly.txt'
      assert_includes response.body, 'Permission denied'
      assert_includes response.body, '$ false'
      assert_includes response.body, 'Command exited with code 1'
    end
  end

  def test_renders_pending_duplicate_only_tool_calls_with_empty_body
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "toolCall", id: "pending-bash", name: "bash", arguments: { command: "pwd" } },
              { type: "toolCall", id: "pending-read", name: "read", arguments: { path: "README.md" } },
              { type: "toolCall", id: "pending-edit", name: "edit", arguments: { path: "TODO.md", edits: [] } },
              { type: "toolCall", id: "pending-write", name: "write", arguments: { path: "empty.txt", content: "" } },
              { type: "toolCall", id: "pending-edit-preview", name: "edit", arguments: { path: "PLAN.md", edits: [{ oldText: "old", newText: "new" }] } },
              { type: "toolCall", id: "pending-write-preview", name: "write", arguments: { path: "notes.txt", content: "hello" } }
            ]
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)

      ["$ pwd", "read README.md", "edit TODO.md", "write empty.txt"].each do |summary|
        tool_card = compact_card_with_summary(document, summary)

        assert tool_card, "Expected compact card for #{summary}"
        assert_nil tool_card.at_css(".message-body")
      end

      assert_equal ["Edit 1", "- old", "+ new"], compact_card_with_summary(document, "edit PLAN.md").css(".tool-diff-line").map(&:text)
      assert_equal ["+ hello"], compact_card_with_summary(document, "write notes.txt").css(".tool-diff-line").map(&:text)
    end
  end

  def test_pairs_historical_bash_tool_call_with_matching_result
    Dir.mktmpdir do |dir|
      tool_call_id = "call_123"
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "thinking", thinking: "" },
              { type: "toolCall", id: tool_call_id, name: "bash", arguments: { command: "git status --short", timeout: 30 } }
            ]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "toolResult",
            toolCallId: tool_call_id,
            toolName: "bash",
            content: [{ type: "text", text: " M app.rb" }],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      bash_card = document.css(".message--compact").find do |card|
        card.at_css(".compact-summary")&.text == "$ git status --short (timeout 30s)"
      end

      assert bash_card
      assert_equal " M app.rb", bash_card.at_css(".message-body").text
      refute_includes response.body, "Raw details"
      refute_includes response.body, '&quot;type&quot;: &quot;toolCall&quot;'
      refute_includes response.body, '&quot;toolCallId&quot;: &quot;call_123&quot;'
      refute_includes response.body, "[thinking]"
      refute_includes response.body, '<div class="message-details-summary"><span class="compact-summary">bash</span></div>'
    end
  end

  def test_historical_subagent_tool_call_is_hidden_when_result_renders_output
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [
              { type: "toolCall", id: "call-1", name: "subagent", arguments: { agent: "reviewer", task: "Review the diff" } }
            ]
          }
        },
        {
          type: "message",
          timestamp: "2026-06-13T10:01:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "No findings." }],
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      subagent_cards = document.css(".message--compact").select do |card|
        card.at_css(".compact-summary")&.text == "subagent"
      end

      assert_equal 1, subagent_cards.length
      assert_equal "Review the diff", subagent_cards.first.at_css("[data-subagent-prompt-preview]").text
      assert_nil subagent_cards.first.at_css("[data-subagent-prompt-body]")
      assert_nil subagent_cards.first.at_css("[data-subagent-prompt]")["open"]
      assert_equal "No findings.", subagent_cards.first.at_css(".message-body").text
      assert_equal Time.parse("2026-06-13T10:00:00Z").localtime.strftime("%Y-%m-%d %H:%M"), subagent_cards.first.at_css(".message-meta").text
      refute_includes response.body, "[tool: subagent]"
    end
  end

  def test_historical_general_subagent_result_renders_transcript_and_usage
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-07-10T19:42:00Z",
          message: {
            role: "toolResult",
            toolCallId: "call-1",
            toolName: "subagent",
            content: [{ type: "text", text: "Largest: `/home/vitek/.hermes` — **7.4G**." }],
            details: {
              task: "Find the largest directory",
              cwd: "/home/vitek",
              model: "openai-codex/gpt-5.6-sol",
              status: "done",
              tools: [{
                id: "child-call-1",
                name: "bash",
                args: { command: "du -shx /home/vitek/.hermes", timeout: 120 },
                status: "done",
                output: "7.4G\t/home/vitek/.hermes\n"
              }],
              textItems: ["Largest: `/home/vitek/.hermes` — **7.4G**."],
              streamingText: "",
              usage: { input: 6_523, output: 332, cacheRead: 1_536, cacheWrite: 0, cost: 0.043343, contextTokens: 2_854, turns: 3 }
            },
            isError: false
          }
        }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      card = document.css(".message--compact").find { |entry| entry.at_css(".compact-summary")&.text == "subagent general" }
      refute_nil card
      assert_equal "Find the largest directory", card.at_css("[data-subagent-prompt-preview]").text
      assert_nil card.at_css("[data-subagent-prompt-body]")
      assert_includes card.at_css(".message-body").text, "✓ general"
      assert_includes card.at_css(".message-body").text, "$ du -shx ~/.hermes"
      assert_includes card.at_css(".message-body").text, "3 turns ↑6.5k ↓332 R1.5k $0.0433 ctx:2.9k openai-codex/gpt-5.6-sol"
    end
  end

  def test_live_event_script_supports_compact_tool_rendering
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function contentSegments(content, message = {})"
      assert_includes APP_JAVASCRIPT, "appendCompactMessage(roleName, segment.summary, segment.text, true"
      refute_includes APP_JAVASCRIPT, "segment.expanded"
      refute_includes APP_JAVASCRIPT, "rawDetails"
      refute_includes response.body, "Raw details"
      assert_includes APP_JAVASCRIPT, "renderToolSummary(container, parts, fallback)"
      assert_includes APP_JAVASCRIPT, "message--tool-transcript"
      assert_includes APP_JAVASCRIPT, "toolSummaryParts(toolName, toolPart?.arguments || {})"
      assert_includes APP_JAVASCRIPT, "function transcriptToolCallText(name, args = {})"
      assert_includes APP_JAVASCRIPT, 'if (["bash", "read"].includes(part.name)) return "";'
      assert_includes APP_JAVASCRIPT, 'if (["edit", "write"].includes(part.name)) return transcriptToolCallText(part.name, part.arguments || {});'
      assert_includes APP_JAVASCRIPT, 'return editPreview;'
      assert_includes APP_JAVASCRIPT, '}).filter((segment) => segment.text || segment.compact || segment.images.length > 0);'
      assert_includes APP_JAVASCRIPT, 'if (lines[lines.length - 1] === "") lines.pop();'
      assert_includes APP_JAVASCRIPT, 'renderToolTranscriptBody(body, text, toolName = "", options = {})'
      assert_includes APP_JAVASCRIPT, 'body.dataset.rawText = text || "";'
      assert_includes APP_JAVASCRIPT, 'body.classList.toggle("message-body--edit-preview", preview);'
      assert_includes APP_JAVASCRIPT, 'const hasText = rawText !== "";'
      assert_includes APP_JAVASCRIPT, 'collapse.hidden = !hasText;'
      assert_includes APP_JAVASCRIPT, 'if (!hasText) {'
      assert_includes APP_JAVASCRIPT, 'const shouldCollapse = collapse.dataset.toolOutputCollapsible === "true" && lines.length > TOOL_OUTPUT_DESKTOP_TAIL_LINES;'
      assert_includes APP_JAVASCRIPT, 'fullTemplate?.content.replaceChildren(...this.toolOutputLineNodes(lines, toolName, preview, 0));'
      assert_includes APP_JAVASCRIPT, 'tailTemplate?.content.replaceChildren(...this.toolOutputLineNodes(tailLines, toolName, preview, desktopExtraCount));'
      assert_includes APP_JAVASCRIPT, 'control.hidden = true;'
      assert_includes APP_JAVASCRIPT, 'span.className = `tool-diff-line ${this.toolDiffLineClass(line, preview)}`;'
      assert_includes APP_JAVASCRIPT, 'renderToolTranscriptBody(entry.body, segment.text, segment.toolName || entry.toolName, { preview: segment.toolPreview === true });'
      assert_includes APP_JAVASCRIPT, 'toolPreview: toolPart?.type === "toolCall" && toolName === "edit"'
      assert_includes APP_JAVASCRIPT, 'pairedToolCallEntry.body.classList.contains("message-body--edit-preview")'
      assert_includes APP_JAVASCRIPT, 'segment.toolName === "bash" || (segment.toolTranscript && segment.error !== true && segment.toolName !== "write") ? segment.text'
      assert_includes APP_JAVASCRIPT, '[pairedToolCallEntry.body.dataset.rawText, segment.text].filter(Boolean).join("\\n\\n")'
      refute_includes APP_JAVASCRIPT, 'details.open = options.open === true;'
      refute_includes APP_JAVASCRIPT, 'collapseButton.textContent = "▴ Collapse details";'
      refute_includes APP_JAVASCRIPT, 'event.target.closest("[data-collapse-details]")'
      assert_includes APP_JAVASCRIPT, 'error: message.isError === true'
      refute_includes APP_JAVASCRIPT, 'open: segment.expanded'
      assert_includes APP_JAVASCRIPT, 'error: segment.error'
      assert_includes APP_JAVASCRIPT, 'PAIRED_TOOL_NAMES.has(segment.toolName)'
      assert_includes APP_JAVASCRIPT, "part.type === \"toolCall\""
      assert_includes APP_JAVASCRIPT, "part.type === \"thinking\""
      assert_includes APP_JAVASCRIPT, "function subagentToolCall(part)"
      assert_includes APP_JAVASCRIPT, "if (subagentToolCall(part)) return;"
      assert_includes APP_JAVASCRIPT, "renderSubagentPrompt(entry, prompt)"
      assert_includes APP_JAVASCRIPT, "subagentPromptFromEvent(event)"
      assert_includes APP_JAVASCRIPT, "subagentPromptFromDetails(message.details)"
    end
  end

  def test_live_event_script_renders_tool_execution_updates
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "renderToolExecutionEvent(event, timestamp = eventTimestamp(event)"
      assert_includes APP_JAVASCRIPT, "this.parser.toolExecutionText(event)"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.renderToolExecutionEvent(event);"
      assert_includes APP_JAVASCRIPT, "if (!event.toolCallId || PAIRED_TOOL_NAMES.has(event.toolName)) return;"
    end
  end

  def test_live_event_script_renders_subagent_progress_open_while_running
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function subagentDetailsFromEvent(event)"
      assert_includes APP_JAVASCRIPT, "function subagentDisplayText(details, fallback, running = false, preferFallback = false)"
      assert_includes APP_JAVASCRIPT, 'function generalSubagentDisplayText(details, fallback = "", preferFallback = false)'
      assert_includes APP_JAVASCRIPT, "function generalSubagentDetails(details)"
      assert_includes APP_JAVASCRIPT, "function subagentResultRunning(details, result, index, running)"
      assert_includes APP_JAVASCRIPT, 'if (result.stopReason === "stop") return false;'
      assert_includes APP_JAVASCRIPT, 'if (event.toolName === "subagent")'
      refute_includes APP_JAVASCRIPT, 'entry.details.open = subagentRunning(event);'
      refute_includes APP_JAVASCRIPT, 'open: event.toolName === "subagent" && subagentRunning(event)'
      assert_includes APP_JAVASCRIPT, 'if (event.toolName === "subagent") return subagentSummary(subagentDetailsFromEvent(event), subagentRunning(event));'
      assert_includes APP_JAVASCRIPT, 'if (generalSubagentDetails(details)) return generalSubagentDisplayText(details, running ? "" : fallback, preferFallback);'
      assert_includes APP_JAVASCRIPT, 'if (generalSubagentDetails(details)) return "subagent general";'
      assert_includes APP_JAVASCRIPT, 'const freshSubagentDetails = segment.toolName === "subagent" && this.parser.richSubagentDetails(message.details);'
      assert_includes APP_JAVASCRIPT, 'const subagentDetails = segment.toolName === "subagent" ? this.retainSubagentDetails(toolExecutionEntry, message.details, message.isError ? "error" : "done") : null;'
      assert_includes APP_JAVASCRIPT, 'const resultText = subagentDetails ? this.parser.subagentDisplayText(subagentDetails, segment.text, false, !freshSubagentDetails) : segment.text;'
      assert_includes APP_JAVASCRIPT, 'const resultSummary = subagentDetails ? this.parser.subagentSummary(subagentDetails, false) : segment.summary;'
      assert_includes APP_JAVASCRIPT, "retainSubagentDetails(entry, details, finalStatus = null)"
      assert_includes APP_JAVASCRIPT, "entry.subagentDetails"
      assert_includes APP_JAVASCRIPT, 'if (part.type === "toolCall") return items.push(`→ ${formatToolCallPlain(part.name, part.arguments || {})}`);'
    end
  end

  def test_live_event_script_keeps_active_sessions_pinned_after_layout
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "this.autoScrollEnabled = true;"
      assert_includes APP_JAVASCRIPT, "this.forceBottomAutoScroll = false;"
      assert_includes APP_JAVASCRIPT, "this.followOversizedMessageBottom = false;"
      assert_includes APP_JAVASCRIPT, "this.programmaticScroll = false;"
      assert_includes APP_JAVASCRIPT, "nearTop()"
      assert_includes APP_JAVASCRIPT, "latestReadableAssistantMessageIsVisible()"
      assert_includes APP_JAVASCRIPT, "applyAutoScroll(behavior = \"auto\")"
      assert_includes APP_JAVASCRIPT, "positionInitialAtBottom()"
      assert_includes APP_JAVASCRIPT, "conversationController.positionInitialAtBottom();"
      refute_includes APP_JAVASCRIPT, "conversationController.loadOlderHistory().catch(() => {});"
      assert_includes APP_JAVASCRIPT, "this.frame(() => this.frame"
      assert_includes APP_JAVASCRIPT, "latestReadableAssistantMessage()"
      assert_includes APP_JAVASCRIPT, "latestMessageElement()"
      assert_includes APP_JAVASCRIPT, "if (!this.forceBottomAutoScroll && !this.followOversizedMessageBottom && latestAssistant && latestAssistant === this.latestMessageElement() && latestAssistant.offsetHeight > this.element.clientHeight)"
      assert_includes APP_JAVASCRIPT, "this.autoScrollEnabled = this.nearBottom();"
      assert_includes APP_JAVASCRIPT, "if (this.conversationController.autoScrollEnabled && job.body.closest(\".message\") === this.conversationController.latestReadableAssistantMessage())"
      assert_includes APP_JAVASCRIPT, "if (shouldScroll && this.autoScrollEnabled) this.scheduleAutoScroll();"
      assert_includes APP_JAVASCRIPT, "forceInitialBottomFollow()"
      assert_includes APP_JAVASCRIPT, "scrollToTop"
      refute_includes APP_JAVASCRIPT, "const turnButton = event.target.closest(\".message-turn-button\");"
      refute_includes APP_JAVASCRIPT, "turnButton.dataset.direction === \"previous\""
      refute_includes APP_JAVASCRIPT, "scrollToUserMessage(target);"
      refute_includes APP_JAVASCRIPT, "function topJumpControlsOffset()"
      refute_includes APP_JAVASCRIPT, "return remSize * 3.5;"
      assert_includes APP_JAVASCRIPT, "const latestOversizedAssistant = target === latestAssistant && target === this.latestMessageElement();"
      assert_includes APP_JAVASCRIPT, "this.autoScrollEnabled = latestOversizedAssistant;"
      assert_includes APP_JAVASCRIPT, "scrollToBottom(behavior = \"auto\", { force = false } = {})"
      assert_includes APP_JAVASCRIPT, "this.autoScrollEnabled = true;"
      assert_includes APP_JAVASCRIPT, "this.forceBottomAutoScroll = force;"
      assert_includes APP_JAVASCRIPT, "if (this.jumpToLatestButton.dataset.jumpTarget === \"message\") this.scrollToMessageBottom();"
    end
  end

  def test_live_event_script_dedupes_events_already_rendered_from_history
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [{ role: "assistant", text: "Already shown" }])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "data-message-fingerprint=\"assistant:"
      assert_includes APP_JAVASCRIPT, "function messageTimestampKey(timestamp)"
      assert_includes APP_JAVASCRIPT, "function messageFingerprint(roleName, text, timestampKey)"
      assert_includes APP_JAVASCRIPT, "liveMessageAlreadyRendered(roleName, text, timestampKey)"
      assert_includes APP_JAVASCRIPT, "if (live && this.liveMessageAlreadyRendered(roleName, text, timestampKey)) return null;"
      assert_includes APP_JAVASCRIPT, "markLiveEntryRendered(entry, roleName, text, timestamp = null)"
      assert_includes APP_JAVASCRIPT, "entry.article.remove();"
      assert_includes APP_JAVASCRIPT, "forgetLiveEntry(entry)"
      assert_includes APP_JAVASCRIPT, "this.liveAssistantSegments.delete(key);"
      assert_includes APP_JAVASCRIPT, "this.livePairedToolCalls.delete(key);"
      assert_includes APP_JAVASCRIPT, "this.markLiveEntryRendered(pairedToolCallEntry, pairedToolCallEntry.article.dataset.role || \"assistant\", mergedText)"
      assert_includes APP_JAVASCRIPT, "article.dataset.messageTimestamp = timestampKey;"
    end
  end

  def test_live_script_supports_control_session_shortcuts
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function enterSessionShortcutMode()"
      assert_includes APP_JAVASCRIPT, "function openNewSessionModal()"
      assert_includes APP_JAVASCRIPT, "isCtrlOrMetaShortcut(event, \"n\")"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"pi:new-session-requested\""
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"pi:desktop-server-activated\""
      assert_includes APP_JAVASCRIPT, "focusPromptAfterDesktopServerActivation"
      assert_includes APP_JAVASCRIPT, 'event.key === "Control"'
      assert_includes APP_JAVASCRIPT, "if (event.altKey || !event.ctrlKey) return;"
      assert_includes APP_JAVASCRIPT, 'if (event.key === "Control") exitSessionShortcutMode();'
      refute_includes APP_JAVASCRIPT, "function sessionShortcutModifierKey()"
      refute_includes APP_JAVASCRIPT, "navigator.userAgentData?.platform || navigator.platform"
      assert_includes APP_JAVASCRIPT, "function recentSessionShortcutFromEvent(event)"
      assert_includes APP_JAVASCRIPT, "event.code.match(/^Digit([1-9])$/)"
      assert_includes APP_JAVASCRIPT, "event.code.match(/^Numpad([1-9])$/)"
      assert_includes APP_JAVASCRIPT, "if (shortcut) {\n    event.preventDefault();\n    if (event.repeat) return;\n    openRecentSessionShortcut(shortcut)"
      assert_includes APP_JAVASCRIPT, "function currentSessionPath()"
      assert_includes APP_JAVASCRIPT, "window.location.href = link.href;"
      refute_includes APP_JAVASCRIPT, "clearUnreadSession(link.dataset.sessionPath)"
      assert_includes APP_JAVASCRIPT, "exitSessionShortcutMode();\n  if (!link || !normalLeftClick(event)) return;"
      assert_includes APP_JAVASCRIPT, "document.addEventListener(\"keyup\", (event) => {"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"blur\", exitSessionShortcutMode);"
      refute_includes APP_JAVASCRIPT, "sessionShortcutTimer = setTimeout(exitSessionShortcutMode, 5000);"
      assert_includes APP_JAVASCRIPT, "session-shortcuts-visible"
      assert_includes APP_JAVASCRIPT, "if (wasVisible) sidebarController.scheduleRefresh(0);"
    end
  end

  def test_live_script_refreshes_sidebar_without_switching_sessions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "export class SidebarController"
      assert_includes APP_JAVASCRIPT, "fragmentUrl(url = this.window.location.href)"
      assert_includes APP_JAVASCRIPT, "scrollContainer()"
      assert_includes APP_JAVASCRIPT, "bindInteractionTracking()"
      assert_includes APP_JAVASCRIPT, "recentlyInteracted()"
      assert_includes APP_JAVASCRIPT, "async refresh()"
      assert_includes APP_JAVASCRIPT, "async loadMore(button)"
      assert_includes APP_JAVASCRIPT, "this.asyncEpoch = 0;"
      assert_includes APP_JAVASCRIPT, "const epoch = ++this.asyncEpoch;"
      assert_includes APP_JAVASCRIPT, "button.classList.add(\"is-loading\");"
      assert_includes APP_JAVASCRIPT, "if (!this.current(epoch, boundElement)) return;"
      assert_includes APP_JAVASCRIPT, "this.replace(html, { scrollTop: previousScrollTop });"
      notification_capture = 'const notificationToggle = oldElement.querySelector("[data-notification-toggle]");'
      sidebar_replacement = "oldElement.outerHTML = html;"
      notification_reinsertion = 'this.element.querySelector("[data-notification-toggle]")?.replaceWith(notificationToggle);'
      assert_includes APP_JAVASCRIPT, notification_capture
      assert_includes APP_JAVASCRIPT, sidebar_replacement
      assert_includes APP_JAVASCRIPT, notification_reinsertion
      assert_operator APP_JAVASCRIPT.index(notification_capture), :<, APP_JAVASCRIPT.index(sidebar_replacement)
      assert_operator APP_JAVASCRIPT.index(sidebar_replacement), :<, APP_JAVASCRIPT.index(notification_reinsertion)
      assert_includes APP_JAVASCRIPT, "if (this.controlsActive() || this.recentlyInteracted())"
      assert_includes APP_JAVASCRIPT, "fetch(this.fragmentUrl())"
      assert_includes APP_JAVASCRIPT, "const refreshedScrollContainer = this.scrollContainer();"
      assert_includes APP_JAVASCRIPT, "this.bindInteractionTracking();"
      assert_includes APP_JAVASCRIPT, "setTimeout(() => this.refresh().catch(() => {}), delay)"
    end
  end

  def test_session_switching_script_replaces_session_regions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function bindSessionDom()"
      assert_includes APP_JAVASCRIPT, "function switchSession(url, { push = true, focus = true, preserveScroll = false } = {})"
      assert_includes APP_JAVASCRIPT, "const switchGeneration = ++sessionSwitchGeneration;\n  const refreshRequestVersion = sidebarController.refreshRequestVersion;\n  let navigatingAway = false;\n  showSessionSwitching();\n  resetSessionViewState();"
      assert_includes APP_JAVASCRIPT, "if (refreshRequestVersion !== sidebarController.refreshRequestVersion) sidebarController.scheduleRefresh(0);"
      assert_includes APP_JAVASCRIPT, "fetch(sessionFragmentUrl(url), { headers: { \"Accept\": \"application/json\" } })"
      assert_includes APP_JAVASCRIPT, "if (switchGeneration !== sessionSwitchGeneration) return false;"
      assert_includes APP_JAVASCRIPT, "if (link.classList.contains(\"selected\")) {\n    sidebarController.closeMobile();\n    return;\n  }"
      assert_includes APP_JAVASCRIPT, "closeMobile()"
      refute_includes APP_JAVASCRIPT, "const previousSidebarScrollTop = sidebarScrollContainer()?.scrollTop || 0;"
      assert_includes APP_JAVASCRIPT, "sidebarController.replace(payload.sidebar_html, { notify: false });"
      assert_includes APP_JAVASCRIPT, "conversationPanel.outerHTML = payload.conversation_html;"
      refute_includes APP_JAVASCRIPT, "if (refreshedSidebarScrollContainer) refreshedSidebarScrollContainer.scrollTop = previousSidebarScrollTop;"
      assert_includes APP_JAVASCRIPT, "history.pushState({ session: payload.session }"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"popstate\", () => switchSession(window.location.href, { push: false, focus: true }));"
      assert_includes APP_JAVASCRIPT, "sidebarController.closeMobile();"
    end
  end

  def test_session_switching_script_resets_polling_and_attachments
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "const generation = sessionViewGeneration;"
      assert_includes APP_JAVASCRIPT, "const submittedViewChanged = () => generation !== sessionViewGeneration || switchGeneration !== sessionSwitchGeneration || submittedSession !== promptSessionInput?.value;"
      assert_includes APP_JAVASCRIPT, "if (stopHandlingChangedSubmittedView()) return;"
      assert_includes APP_JAVASCRIPT, "function refreshSessionStatus(generation = sessionViewGeneration)"
      assert_includes APP_JAVASCRIPT, "function renderModelStatus()"
      assert_includes APP_JAVASCRIPT, "[liveStatusModel, liveStatusThinking ? `(${liveStatusThinking})` : null]"
      assert_includes APP_JAVASCRIPT, "removeStatusItem(\"thinking\")"
      assert_includes APP_JAVASCRIPT, "if (!response.ok || generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;"
      assert_includes APP_JAVASCRIPT, "refreshSessionStatus(generation).catch(() => {});"
      assert_includes APP_JAVASCRIPT, "function resetSessionViewState()"
      assert_includes APP_JAVASCRIPT, "markOptimisticUserMessageFailed(text)"
      assert_includes APP_JAVASCRIPT, "const previousWaitingForOutputSince = waitingForOutputSince;"
      assert_includes APP_JAVASCRIPT, "const submittedImageFiles = pendingImages.map((entry) => entry.file);"
      assert_includes APP_JAVASCRIPT, "submittedImageFiles.forEach((file) => formData.append(\"images[]\", file, file.name || \"image\"));"
      assert_includes APP_JAVASCRIPT, "if (cloneCommand && payload?.cancelled) {\n      restoreSubmittedComposerInput();\n      setComposerState(\"idle\");\n      showStatus(\"Clone cancelled\", true);\n      return;\n    }\n    showPromptFailure(payload?.error || \"Prompt failed to send\");"
      assert_includes APP_JAVASCRIPT, "function persistStoredComposerDraft()"
      assert_includes APP_JAVASCRIPT, "const restoreSubmittedComposerInput = () => {"
      assert_includes APP_JAVASCRIPT, "promptTextarea.value = message;\n      persistStoredComposerDraft();"
      assert_includes APP_JAVASCRIPT, "pendingImages = submittedImageFiles.map((file) => ({ file, url: URL.createObjectURL(file) }));"
      assert_includes APP_JAVASCRIPT, "renderAttachments();"
      assert_includes APP_JAVASCRIPT, "setComposerState(\"running\", \"Pi is running…\", { since: previousWaitingForOutputSince });"
      assert_includes APP_JAVASCRIPT, "markOptimisticUserMessageFailed(message);"
      assert_includes APP_JAVASCRIPT, "message.hasAttribute(\"data-optimistic-text\") ? message.dataset.optimisticText : message.querySelector(\".message-body\")?.textContent"
      assert_includes APP_JAVASCRIPT, 'return targetText.startsWith(`${optimisticText}\\n`);'
      assert_includes APP_JAVASCRIPT, "appendMessage(\"assistant\", `Prompt failed to send:\\n\\n${errorMessage}`, true, true, new Date(), { finalAssistantResponse: true });"
      assert_includes APP_JAVASCRIPT, "clearTimeout(eventPollTimer);"
      assert_includes APP_JAVASCRIPT, "eventPollInFlight = false;"
      assert_includes APP_JAVASCRIPT, "sessionViewGeneration += 1;"
      assert_includes APP_JAVASCRIPT, "if (!response.ok || generation !== sessionViewGeneration) return;"
      assert_includes APP_JAVASCRIPT, "if (generation === sessionViewGeneration) {"
      assert_includes APP_JAVASCRIPT, "scheduleNextEventPoll(nextEventPollDelay(!pollSucceeded));"
      assert_includes APP_JAVASCRIPT, "resetLiveAssistantTracking();"
      assert_includes APP_JAVASCRIPT, "resetEventPollBackoff();"
      assert_includes APP_JAVASCRIPT, "stopWaitingForOutput();"
      assert_includes APP_JAVASCRIPT, "lastEventSeq = 0;"
      assert_includes APP_JAVASCRIPT, "autoScrollEnabled = true;"
      assert_includes APP_JAVASCRIPT, "clearAttachments();"
    end
  end

  def test_completed_prompt_refreshes_sidebar_after_switching_sessions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      handler_start = APP_JAVASCRIPT.index("const stopHandlingChangedSubmittedView = () => {")
      handler_end = APP_JAVASCRIPT.index("      };", handler_start)
      handler = APP_JAVASCRIPT[handler_start..handler_end]
      assert_includes handler, "if (!submittedViewChanged()) return false;"
      assert_includes handler, "sidebarController.requestRefresh();"
      assert_includes handler, "return true;"
      assert_operator APP_JAVASCRIPT.scan("if (stopHandlingChangedSubmittedView()) return;").length, :>=, 3
    end
  end

  def test_session_switching_script_intercepts_new_session_forms
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "event.target.closest('form[action=\"/sessions/new\"]')"
      assert_includes APP_JAVASCRIPT, "headers: { \"Accept\": \"application/json\" }"
      assert_includes APP_JAVASCRIPT, "const switchGeneration = sessionSwitchGeneration;"
      assert_includes APP_JAVASCRIPT, "const viewGeneration = sessionViewGeneration;"
      assert_includes APP_JAVASCRIPT, "showSessionSwitching();"
      assert_includes APP_JAVASCRIPT, "if (switchGeneration !== sessionSwitchGeneration || viewGeneration !== sessionViewGeneration) return;"
      assert_includes APP_JAVASCRIPT, "await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`"
    end
  end

  def test_live_output_starts_polling_after_current_rpc_event_cursor
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, FakeRpcClient.new(calls, [{ "type" => "old" }]))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "data-events-after=\"1\""
      assert_includes APP_JAVASCRIPT, "function resetEventCursor()"
      assert_includes APP_JAVASCRIPT, "lastEventSeq = Number(liveOutput?.dataset.eventsAfter || 0);"
      assert_includes APP_JAVASCRIPT, "resetEventCursor();"
    end
  end

  def test_live_output_restores_running_tool_progress_with_the_current_event_cursor
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        {
          type: "message",
          timestamp: "2026-06-13T10:00:00Z",
          message: {
            role: "assistant",
            content: [{ type: "toolCall", id: "call-1", name: "subagent", arguments: { agent: "reviewer", task: "Review the diff" } }]
          }
        }
      ])
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      client = FakeRpcClient.new([], [{ "type" => "old" }])
      def client.live_snapshot
        {
          event_sequence: 7,
          active_tool_events: [
            {
              "type" => "tool_execution_update",
              "toolCallId" => "call-1",
              "toolName" => "subagent",
              "partialResult" => { "content" => [{ "type" => "text", "text" => "Reviewing" }] }
            }
          ]
        }
      end
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      live_output = document.at_css("#live-output")
      assert_equal "7", live_output["data-events-after"]
      assert_equal "call-1", JSON.parse(live_output["data-active-tool-events"]).first["toolCallId"]
      assert_equal({ "call-1" => "2026-06-13T10:00:00.000Z" }, JSON.parse(live_output["data-active-tool-timestamps"]))
      assert_equal({ "call-1" => "Review the diff" }, JSON.parse(live_output["data-active-tool-prompts"]))
      assert_includes APP_JAVASCRIPT, "restoreActiveToolExecutions()"
      assert_includes APP_JAVASCRIPT, "restoreActiveToolExecutions();"
      assert_includes APP_JAVASCRIPT, "events.forEach((event) => this.renderToolExecutionEvent(event, timestamps[event.toolCallId], false, prompts[event.toolCallId]));"
      assert_includes APP_JAVASCRIPT, "formatTimestamp(timestamp, options.timestampFallback !== false)"
    end
  end

  def test_live_event_script_schedules_non_overlapping_polls
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "let eventPollInFlight = false;"
      assert_includes APP_JAVASCRIPT, "let lastEventSeq = 0;"
      assert_includes APP_JAVASCRIPT, "let waitingForOutputSince = null;"
      assert_includes APP_JAVASCRIPT, "let emptyEventPollCount = 0;"
      assert_includes APP_JAVASCRIPT, "function scheduleNextEventPoll(delay = nextEventPollDelay())"
      assert_includes APP_JAVASCRIPT, "if (eventPollInFlight) return;"
      assert_includes APP_JAVASCRIPT, "eventPollInFlight = true;"
      assert_includes APP_JAVASCRIPT, "const eventsUrl = new URL(liveOutput.dataset.eventsUrl, window.location.origin);"
      assert_includes APP_JAVASCRIPT, "eventsUrl.searchParams.set(\"after\", lastEventSeq);"
      assert_includes APP_JAVASCRIPT, "lastEventSeq = payload.last_seq;"
      assert_includes APP_JAVASCRIPT, "if (payload.missed) {"
      assert_includes APP_JAVASCRIPT, "await refreshCurrentSessionPreservingComposer();"
      assert_includes APP_JAVASCRIPT, "eventPollInFlight = false;"
      assert_includes APP_JAVASCRIPT, "return !document.hidden && sessionSyncBlocked() ? Math.min(delay, 1000) : delay;"
      assert_includes APP_JAVASCRIPT, "scheduleNextEventPoll(nextEventPollDelay(!pollSucceeded));"
      assert_includes APP_JAVASCRIPT, 'if (composerState && state === "running" && previousState !== "running") {'
      assert_includes APP_JAVASCRIPT, "sidebarController.requestRefresh();"
      assert_includes APP_JAVASCRIPT, "emptyEventPollCount = payload.events.length > 0 ? 0 : emptyEventPollCount + 1;"
      assert_includes APP_JAVASCRIPT, "resetEventPollBackoff();"
      assert_includes APP_JAVASCRIPT, "startWaitingForOutput();"
      assert_includes APP_JAVASCRIPT, "stopWaitingForOutput();"
      assert_includes APP_JAVASCRIPT, "scheduleNextEventPoll(0);"
      assert_includes APP_JAVASCRIPT, "let eventPollAbortController = null;"
      assert_includes APP_JAVASCRIPT, "const pollTimeout = setTimeout(() => controller.abort(), 12000);"
      assert_includes APP_JAVASCRIPT, "updateWaitingForOutputStatus();"
      assert_includes APP_JAVASCRIPT, "signal: controller.signal"
      assert_includes APP_JAVASCRIPT, "const STALE_SESSION_REFRESH_AFTER_MS = 60 * 1000;"
      assert_includes APP_JAVASCRIPT, "let staleSessionRefreshInFlight = false;"
      assert_includes APP_JAVASCRIPT, "async function refreshStaleSessionAfterResume(hiddenDuration = 0)"
      assert_includes APP_JAVASCRIPT, "if (staleSessionRefreshInFlight) return true;"
      assert_includes APP_JAVASCRIPT, "const pollingGap = Date.now() - lastEventPollSuccessAt;"
      assert_includes APP_JAVASCRIPT, "if (hiddenDuration < STALE_SESSION_REFRESH_AFTER_MS && pollingGap < STALE_SESSION_REFRESH_AFTER_MS) return false;"
      assert_includes APP_JAVASCRIPT, "staleSessionRefreshInFlight = true;"
      assert_includes APP_JAVASCRIPT, "return await refreshCurrentSessionPreservingComposer();"
      assert_includes APP_JAVASCRIPT, "staleSessionRefreshInFlight = false;"
      assert_includes APP_JAVASCRIPT, "async function resumeEventPolling(hiddenDuration = 0)"
      assert_includes APP_JAVASCRIPT, "abortEventPoll();"
      assert_includes APP_JAVASCRIPT, "if (await refreshStaleSessionAfterResume(hiddenDuration)) return;"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"pageshow\", () => resumeEventPolling().catch(() => {}));"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"focus\", () => {"
      assert_includes APP_JAVASCRIPT, "resumeEventPolling().catch(() => {});"
      assert_includes APP_JAVASCRIPT, "window.addEventListener(\"online\", () => resumeEventPolling().catch(() => {}));"
      refute_includes APP_JAVASCRIPT, "setInterval(() => pollEvents()"
    end
  end

  def test_live_event_script_provides_manual_reconnect_fallback
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "class=\"session-reconnect\""
      assert_includes response.body, "Session may be stale."
      assert_includes APP_JAVASCRIPT, "function showReconnectBanner()"
      assert_includes APP_JAVASCRIPT, "function hideReconnectBanner()"
      assert_includes APP_JAVASCRIPT, "function composerDraft()"
      assert_includes APP_JAVASCRIPT, "message: promptTextarea?.value || \"\""
      assert_includes APP_JAVASCRIPT, "images: pendingImages.map((entry) => entry.file)"
      assert_includes APP_JAVASCRIPT, "function restoreComposerDraft(draft)"
      assert_includes APP_JAVASCRIPT, "if (!draft || promptSessionInput?.value !== draft.session) return;"
      assert_includes APP_JAVASCRIPT, "if (draft.images.length > 0) addImageFiles(draft.images, { restore: true });"
      assert_includes APP_JAVASCRIPT, "function refreshCurrentSessionPreservingComposer()"
      assert_includes APP_JAVASCRIPT, "const refreshed = await switchSession(window.location.href, { push: false, focus: false, preserveScroll: true });"
      assert_includes APP_JAVASCRIPT, "if (refreshed) restoreComposerDraft(draft);"
      assert_includes APP_JAVASCRIPT, "function reconnectSession()"
      assert_includes APP_JAVASCRIPT, "await refreshCurrentSessionPreservingComposer();"
      assert_includes APP_JAVASCRIPT, "reconnectButton?.addEventListener(\"click\", reconnectSession);"
    end
  end

  def test_live_event_script_keeps_assistant_and_status_roles_separate
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      refute_includes APP_JAVASCRIPT, "this.liveAssistantMessage"
      assert_includes APP_JAVASCRIPT, "this.liveAssistantSegments = new Map();"
      assert_includes APP_JAVASCRIPT, "this.liveAssistantSeen = false;"
      assert_includes APP_JAVASCRIPT, "this.liveUserMessages = new Map();"
      assert_includes APP_JAVASCRIPT, "function syncComposerFocus(state = composerState?.dataset.state)"
      assert_includes APP_JAVASCRIPT, "let escapeStopConfirmationExpiresAt = 0;"
      assert_includes APP_JAVASCRIPT, "const ESCAPE_STOP_CONFIRMATION_WINDOW_MS = 2000;"
      assert_includes APP_JAVASCRIPT, "optimisticUserMessage(text)"
      assert_includes APP_JAVASCRIPT, "upsertLiveUserSegment(event, segment, fallbackIndex, shouldScroll, timestamp)"
      assert_includes APP_JAVASCRIPT, 'if (live && roleName === "user" && !options.optimistic && this.optimisticUserMessageAlreadyRendered(text)) return null;'
      assert_includes APP_JAVASCRIPT, 'if (options.optimistic) {'
      assert_includes APP_JAVASCRIPT, "article.dataset.optimisticText = options.optimisticText ?? text;"
      assert_includes APP_JAVASCRIPT, "article.dataset.optimisticImageCount = String(options.images?.length || 0);"
      assert_includes APP_JAVASCRIPT, 'this.upsertLiveUserSegment(event, segment, index, shouldScroll, timestamp);'
      assert_includes APP_JAVASCRIPT, 'const displayText = roleName === "user" && entry.userDisplayText ? entry.userDisplayText : segment.text;'
      assert_includes APP_JAVASCRIPT, 'const entry = { article, body, compact: false, userDisplayText: body?.textContent || segment.text };'
      assert_includes APP_JAVASCRIPT, "function formatTimestamp(timestamp, fallbackToNow = true)"
      assert_includes APP_JAVASCRIPT, "date.getHours()"
      refute_includes APP_JAVASCRIPT, "date.getUTCHours()"
      assert_includes APP_JAVASCRIPT, "function eventTimestamp(event)"
      assert_includes APP_JAVASCRIPT, "function eventErrorText(event)"
      assert_includes APP_JAVASCRIPT, "renderErrorEvent(event)"
      assert_includes APP_JAVASCRIPT, "let liveErrorSeen = false;"
      assert_includes APP_JAVASCRIPT, "liveErrorSeen = true;"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.appendMessage(\"error\", errorText, true, true, eventTimestamp(event));"
      assert_includes APP_JAVASCRIPT, 'this.appendMessage("assistant", segment.text, true, shouldScroll, timestamp, { thinking: segment.thinking, finalAssistantResponse, images: segment.images });'
      assert_includes APP_JAVASCRIPT, 'export class ServerMarkdownRenderer'
      assert_includes APP_JAVASCRIPT, 'body.dataset.rendering = "pending";'
      assert_includes APP_JAVASCRIPT, 'job.controller = new AbortController();'
      assert_includes APP_JAVASCRIPT, 'fetch("/markdown", { method: "POST", body: formData, signal: job.controller.signal })'
      assert_includes APP_JAVASCRIPT, 'if (["custom", "system", "status"].includes(role)) return "status";'
      assert_includes APP_JAVASCRIPT, "function showStatus(_text, _forceScroll = false) {}"
      assert_includes APP_JAVASCRIPT, "showStatus(eventStatusText(event));"
      assert_includes APP_JAVASCRIPT, "renderCompactionEvent(event)"
      assert_includes APP_JAVASCRIPT, "this.appendCompactMessage(\"status\", \"Conversation compacted\", event.summary || \"Compaction completed\""
      assert_includes APP_JAVASCRIPT, "refreshSessionStatus().catch(() => {});"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.renderCompactionEvent(event);"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.resetLiveCompactionTracking();"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.removePendingCompactionMessage();"
      assert_includes APP_JAVASCRIPT, "if (!event.aborted && !liveMessageRenderer.liveCompactionRendered) liveMessageRenderer.renderCompactionEvent(event);"
      assert_includes APP_JAVASCRIPT, "if (/^\\/(?:name|rename)$/.test(trimmed)) return { valid: false };"
      assert_includes APP_JAVASCRIPT, "if (/^\\/(?:name|rename)[ \\t]+[^\\r\\n]+$/.test(trimmed)) return { valid: true };"
      assert_includes APP_JAVASCRIPT, "function sessionNameSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, "function sessionForkSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, "function sessionTreeSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, "function sessionCloneSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, "function sessionNewSlashCommand(message)"
      assert_includes APP_JAVASCRIPT, "function updateSessionHeaderName(name)"
      assert_includes APP_JAVASCRIPT, "function sessionTitleFromEvent(event)"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"session_info\") return event.name;"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"custom\" && event.customType === \"pi-extensions-session-title\") return event.data?.title;"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"custom_message\" && event.customType === \"session-title-update\")"
      assert_includes APP_JAVASCRIPT, "updateSessionHeaderName(sessionTitleFromEvent(event));"
      assert_includes APP_JAVASCRIPT, 'new this.window.CustomEvent("pi:sidebar-selected-title", { detail: { title } })'
      assert_includes APP_JAVASCRIPT, 'document.addEventListener("pi:sidebar-selected-title"'
      assert_includes APP_JAVASCRIPT, "updateSessionHeaderName(event.detail.title);"
      assert_includes APP_JAVASCRIPT, "updateNotificationToggle();"
      assert_includes APP_JAVASCRIPT, "const renameCommand = followUp ? null : sessionNameSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const compactCommand = followUp ? null : sessionCompactSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const forkCommand = followUp ? null : sessionForkSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const treeCommand = followUp ? null : sessionTreeSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const cloneCommand = followUp ? null : sessionCloneSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const newCommand = followUp ? null : sessionNewSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "const modelCommand = followUp ? null : sessionModelSlashCommand(message);"
      assert_includes APP_JAVASCRIPT, "if (!renameCommand && !compactCommand && !forkCommand && !treeCommand && !cloneCommand && !newCommand && !modelCommand) {"
      assert_includes APP_JAVASCRIPT, "if (!followUp) {\n      liveMessageRenderer.resetLiveAssistantTracking();\n      document.querySelectorAll(\".tree-position-banner\").forEach((banner) => banner.remove());\n      const optimisticImages = pendingImages.map"
      assert_includes APP_JAVASCRIPT, "const optimisticImages = pendingImages.map((entry) => ({ src: URL.createObjectURL(entry.file), alt: entry.file.name || \"Attached image\" }));\n      liveMessageRenderer.appendMessage(\"user\", message || `[${imageAttachmentLabel(submittedImageFiles.length)}]`, true, true, new Date(), { optimistic: true, optimisticText: message, images: optimisticImages });\n    }"
      assert_includes APP_JAVASCRIPT, "resetEventPollBackoff();"
      assert_includes APP_JAVASCRIPT, "scheduleNextEventPoll(0);"
      assert_includes APP_JAVASCRIPT, "if (payload?.command === \"rename\") {\n      if (payload.error) {\n        restoreSubmittedComposerInput();\n        setComposerState(\"error\", payload.error);\n        showStatus(payload.error, true);\n        return;\n      }\n      clearStoredComposerDraft(submittedSession);"
      assert_includes APP_JAVASCRIPT, "updateSessionHeaderName(payload.name);\n      setComposerState(\"done\", \"Renamed\");\n      showStatus(eventStatusText({ type: \"session_info\", name: payload.name }), true);"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.appendPendingCompactionMessage(new Date());"
      assert_includes APP_JAVASCRIPT, "sidebarController.markSessionCompacting(submittedSession);"
      assert_includes APP_JAVASCRIPT, "if (payload?.command === \"compact\") {\n      sidebarController.refresh().catch(() => {});\n      if (composerState?.dataset.state === \"sending\") setComposerState(\"running\", \"Compacting…\");\n      showStatus(\"Compaction started\", true);\n      return;\n    }"
      assert_includes APP_JAVASCRIPT, "if (payload?.command === \"fork\") {\n      setComposerState(\"idle\", \"\", { focus: false });\n      showStatus(\"Choose a fork point\", true);\n      openForkSessionModal();\n      return;\n    }"
      assert_includes APP_JAVASCRIPT, "if (payload?.command === \"tree\") {\n      setComposerState(\"idle\", \"\", { focus: false });\n      showStatus(\"Choose a tree entry\", true);\n      openTreeSessionModal();\n      return;\n    }"
      assert_includes APP_JAVASCRIPT, "promptForm.requestSubmit();"
      assert_includes APP_JAVASCRIPT, "function resizePromptTextarea()"
      assert_includes APP_JAVASCRIPT, "commandList?.removeAttribute(\"open\");"
      assert_includes APP_JAVASCRIPT, "function filterCommandsFromPrompt()"
      assert_includes response.body, "Slash commands are not supported in queued follow-up messages."
      assert_includes APP_JAVASCRIPT, "function composingFollowUp()"
      assert_includes APP_JAVASCRIPT, "if (composingFollowUp()) return showQueuedSlashCommandMessage();"
      assert_includes APP_JAVASCRIPT, "const query = promptTextarea.value.startsWith(\"/\") ? promptTextarea.value.slice(1).trim().toLowerCase() : \"\";"
      assert_includes APP_JAVASCRIPT, "function selectHighlightedCommand()"
      assert_includes APP_JAVASCRIPT, "setComposerState(\"running\", \"Pi is running…\");"
      assert_includes APP_JAVASCRIPT, "composerState.textContent = \"Press ESC again to stop current task\";"
      assert_includes APP_JAVASCRIPT, "composerStopButton = document.querySelector(\".session-header .composer-stop-button\") || null;"
      assert_includes APP_JAVASCRIPT, "const agentBusy = [\"running\", \"sending\"].includes(state);"
      assert_includes APP_JAVASCRIPT, "promptTextarea.disabled = submitting || sessionSyncBlocked();"
      assert_includes APP_JAVASCRIPT, "if (focus && state !== previousState) syncComposerFocus(state);"
      assert_includes APP_JAVASCRIPT, "if (focus) syncComposerFocus();"
      assert_includes APP_JAVASCRIPT, "composerStopButton.hidden = !agentBusy;"
      assert_includes APP_JAVASCRIPT, "if (followUp) formData.set(\"streaming_behavior\", \"follow_up\");"
      assert_includes APP_JAVASCRIPT, "const attachmentsDisabled = submitting || sessionSyncBlocked();"
      assert_includes APP_JAVASCRIPT, "addImageFiles(files);"
      assert_includes APP_JAVASCRIPT, "function confirmOrStopRunningTask(event)"
      assert_includes APP_JAVASCRIPT, "if (composerState?.dataset.state !== \"running\") return false;"
      assert_includes APP_JAVASCRIPT, "if (event.repeat) return true;"
      assert_includes APP_JAVASCRIPT, "showStatus(\"Press ESC again to stop current task\", true);"
      assert_includes APP_JAVASCRIPT, "if (composerState) composerState.textContent = \"Stopping current task…\";"
      assert_includes APP_JAVASCRIPT, "abortForm.requestSubmit();"
      assert_includes APP_JAVASCRIPT, "if (event.key === \"Escape\" && confirmOrStopRunningTask(event)) return;"
      assert_includes APP_JAVASCRIPT, "Send follow-up…"
    end
  end

  def test_live_event_script_notifies_when_final_assistant_reply_arrives_outside_active_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function notifyFinalAssistantReply(event)"
      assert_includes APP_JAVASCRIPT, "if (roleName !== \"assistant\" || event.type !== \"message_end\") return;"
      assert_includes APP_JAVASCRIPT, "if (sessionIsActivelyViewed(sessionPath)) return;"
      assert_includes APP_JAVASCRIPT, "const body = notificationReplyPreview(liveMessageParser.finalAssistantReplyText(message));"
      assert_includes APP_JAVASCRIPT, "if (notificationsDisabled()) return;"
      assert_includes APP_JAVASCRIPT, "showPiNotification(name, body, window.location.href, `pi-final-reply:${sessionPath}`)"
      assert_includes APP_JAVASCRIPT, "notifyFinalAssistantReply(event);"
    end
  end

  def test_sidebar_refresh_notifies_for_background_final_replies
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "const previousAssistantCounts = this.assistantResponseCounts(oldElement);"
      assert_includes APP_JAVASCRIPT, "this.notifyBackgroundFinalReplies(previousAssistantCounts);"
      assert_includes APP_JAVASCRIPT, "sessionPath === this.currentSessionPath()"
      assert_includes APP_JAVASCRIPT, 'const key = `${sessionPath}:${currentCount}`;'
      assert_includes APP_JAVASCRIPT, "notificationReplyPreview(link.dataset.latestAssistantResponsePreview)"
      assert_includes APP_JAVASCRIPT, "this.notifyFinalReply(name,"
    end
  end

  def test_live_event_script_updates_streaming_segments_in_place
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "segmentIdentity(event, segment, fallbackIndex)"
      assert_includes APP_JAVASCRIPT, "event.assistantMessageEvent || {}"
      assert_includes APP_JAVASCRIPT, "segment.startIndex ?? update.contentIndex ?? fallbackIndex"
      assert_includes APP_JAVASCRIPT, "upsertLiveAssistantSegment(event, roleName, segment, fallbackIndex, shouldScroll, timestamp)"
      assert_includes APP_JAVASCRIPT, "const existing = this.liveAssistantSegments.get(key);"
      assert_includes APP_JAVASCRIPT, "const updated = this.updateLiveSegment(existing, roleName, segment, shouldScroll, timestamp);"
      assert_includes APP_JAVASCRIPT, "this.liveAssistantSegments.set(key, entry);"
      assert_includes APP_JAVASCRIPT, "this.conversationController.resetOversizedFollow();"
      assert_includes APP_JAVASCRIPT, "this.clearLiveAssistantStreaming();"
      assert_includes APP_JAVASCRIPT, "let liveAgentRunning = false;"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"turn_end\") {"
      assert_includes APP_JAVASCRIPT, "liveMessageRenderer.resetLiveAssistantTracking();"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"agent_settled\") {"
    end
  end

  def test_sidebar_sessions_include_assistant_response_count_for_unread_tracking
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      registry.register(path, FakeRpcClient.new([]))
      PiWebGateway.set :rpc_client_registry, registry
      File.write(path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "thinking", thinking: "not final" }] } }) + "\n", mode: "a")
      File.write(path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "toolCall", name: "bash", arguments: { command: "echo hi" } }] } }) + "\n", mode: "a")
      File.write(path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Final response" }] } }) + "\n", mode: "a")

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "data-session-path=\"#{ERB::Util.html_escape(path)}\""
      assert_includes response.body, "data-assistant-response-count=\"1\""
      assert_includes response.body, "data-latest-assistant-response-preview=\"Final response\""
    end
  end

  def test_sidebar_only_marks_final_answers_unread_when_text_phases_are_available
    Dir.mktmpdir do |dir|
      first_path, second_path = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]
      request = Rack::MockRequest.new(PiWebGateway)
      signature = ->(id, phase) { JSON.generate(v: 1, id: id, phase: phase) }

      request.get("/sidebar", params: { "session" => first_path })
      File.write(second_path, JSON.generate({
        type: "message",
        message: { role: "assistant", content: [{ type: "text", text: "Still working", textSignature: signature.call("progress", "commentary") }] }
      }) + "\n", mode: "a")

      progress_response = request.get("/sidebar", params: { "session" => first_path })
      progress_document = Nokogiri::HTML(progress_response.body)
      progress_link = progress_document.at_css("a.session[data-session-path='#{second_path}']")

      refute progress_link["class"].include?("unread")
      assert_equal "0", progress_link["data-assistant-response-count"]
      assert_equal "", progress_link["data-latest-assistant-response-preview"]

      File.write(second_path, JSON.generate({
        type: "message",
        message: { role: "assistant", content: [{ type: "text", text: "Finished", textSignature: signature.call("answer", "final_answer") }] }
      }) + "\n", mode: "a")

      final_response = request.get("/sidebar", params: { "session" => first_path })
      final_document = Nokogiri::HTML(final_response.body)
      final_link = final_document.at_css("a.session[data-session-path='#{second_path}']")

      assert final_link["class"].include?("unread")
      assert_equal "1", final_link["data-assistant-response-count"]
      assert_equal "Finished", final_link["data-latest-assistant-response-preview"]
    end
  end

  def test_sidebar_tracks_unread_sessions_globally
    Dir.mktmpdir do |dir|
      first_path, second_path = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      initial_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => first_path })
      assert_empty Nokogiri::HTML(initial_response.body).css("a.session.unread")

      File.write(second_path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Done" }] } }) + "\n", mode: "a")
      unread_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => first_path })

      assert_includes unread_response.body, "class=\"session recent-session unread"
      assert_includes unread_response.body, "data-assistant-response-count=\"1\""
      assert_equal "Unread:", Nokogiri::HTML(unread_response.body).at_css("a.session.unread .visually-hidden").text.strip

      assert_includes APP_STYLESHEET, 'a.session.unread .session-title::before { content: "";'
      refute_includes APP_STYLESHEET, "a.session.unread .session-indicators::before"
      refute_includes APP_STYLESHEET, "content: \"new\""
      refute_includes APP_JAVASCRIPT, "localStorage.getItem(\"piSidebarUnreadSessions\")"

      read_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => second_path })
      assert_empty Nokogiri::HTML(read_response.body).css("a.session.unread")
    end
  end

  def test_sidebar_refresh_without_session_param_does_not_clear_background_unread
    Dir.mktmpdir do |dir|
      first_path, second_path = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      initial_response = Rack::MockRequest.new(PiWebGateway).get("/")
      assert_includes APP_JAVASCRIPT, "fragmentUrl(url = this.window.location.href)"
      assert_includes APP_JAVASCRIPT, "if (!sidebarUrl.searchParams.has(\"session\"))"
      assert_includes APP_JAVASCRIPT, "a.session.selected[data-session-path]"

      File.write(first_path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Background done" }] } }) + "\n", mode: "a")
      Rack::MockRequest.new(PiWebGateway).get("/sidebar")

      unread_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => second_path })
      assert_includes unread_response.body, "class=\"session recent-session unread"
    end
  end

  def test_mark_read_endpoint_clears_unread_session_from_other_windows
    Dir.mktmpdir do |dir|
      first_path, second_path = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]
      request = Rack::MockRequest.new(PiWebGateway)

      request.get("/sidebar", params: { "session" => first_path })
      File.write(second_path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Read elsewhere" }] } }) + "\n", mode: "a")
      unread_response = request.get("/sidebar", params: { "session" => first_path })
      assert_includes unread_response.body, "class=\"session recent-session unread"

      mark_response = request.post("/sessions/mark_read", params: { "session" => second_path })
      assert_equal 204, mark_response.status

      read_response = request.get("/sidebar", params: { "session" => first_path })
      assert_empty Nokogiri::HTML(read_response.body).css("a.session.unread")
    end
  end

  def test_session_only_live_script_marks_final_assistant_messages_read
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path, "session_only" => "1" })

      assert_equal 200, response.status
      assert_includes APP_JAVASCRIPT, "function markCurrentSessionRead()"
      assert_includes APP_JAVASCRIPT, "fetch(\"/sessions/mark_read\""
      assert_includes APP_JAVASCRIPT, "if (outcome.finalAssistantEnded) markCurrentSessionRead();"
      assert_includes APP_JAVASCRIPT, "if (document.hidden || !document.hasFocus())"
      assert_includes APP_JAVASCRIPT, "markReadAfterVisible = true;"
      assert_includes APP_JAVASCRIPT, "if (markReadAfterVisible) markCurrentSessionRead();"
    end
  end

  def test_sidebar_hides_persisted_compaction_activity
    Dir.mktmpdir do |dir|
      path = write_session_with_raw_messages(dir, [
        { type: "message", message: { role: "assistant", content: [{ type: "text", text: "Older answer" }] } },
        { type: "compaction", timestamp: "2026-06-13T10:02:00Z", summary: "Important summary\nwith details" }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      link = document.at_css("a.session")
      refute_includes link.text, "Conversation compacted"
      refute_includes link.text, "Important summary with details"
      assert_nil link["data-latest-activity-kind"]
      assert_nil link["data-latest-activity-preview"]
    end
  end

  def test_sidebar_shows_compacting_state_for_active_compaction
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      client = FakeRpcClient.new([])
      def client.compacting? = true
      registry.register(path, client)
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      refute_includes response.body, "Compacting…"
      assert_includes response.body, "session-compacting-indicator"
    end
  end

  def test_sidebar_running_indicator_uses_busy_state
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      idle_client = FakeRpcClient.new([])
      registry.register(path, idle_client)
      PiWebGateway.set :rpc_client_registry, registry

      idle_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })
      refute_includes idle_response.body, "RPC process running"

      busy_client = FakeRpcClient.new([])
      def busy_client.busy? = true
      registry.register(path, busy_client)

      busy_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })
      assert_includes busy_response.body, "Pi is working"
      assert_includes busy_response.body, "session-indicators"
      assert_includes busy_response.body, "session-running-indicator"
    end
  end

  def test_initializes_composer_compacting_state_for_active_compaction
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      client = FakeRpcClient.new([])
      def client.live_snapshot
        {
          event_sequence: 3,
          active_tool_events: [],
          busy: true,
          busy_since: Time.at(1_000),
          compacting: true,
          compacting_since: Time.at(1_005)
        }
      end
      registry.register(path, client)
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/session_fragment",
        params: { "session" => path, "session_only" => "1" }
      )

      assert_equal 200, response.status
      conversation_html = JSON.parse(response.body).fetch("conversation_html")
      assert_includes conversation_html, "data-events-after=\"3\""
      assert_includes conversation_html, "data-composer-state=\"running\""
      assert_includes conversation_html, "data-composer-state-since=\"1005000\""
      assert_includes conversation_html, "data-composer-busy-since=\"1000000\""
      assert_includes conversation_html, "data-composer-compacting=\"true\""
      assert_includes APP_JAVASCRIPT, "const initialComposerCompacting = liveOutput.dataset.composerCompacting === \"true\";"
      assert_includes APP_JAVASCRIPT, "const initialComposerLabel = initialComposerCompacting ? \"Compacting…\" : \"Pi is running…\";"
      assert_includes APP_JAVASCRIPT, "setComposerState(initialComposerState, initialComposerLabel, { since: initialComposerStateSince, focus: false });"
      assert_includes APP_JAVASCRIPT, "if (initialComposerCompacting) liveMessageRenderer.appendPendingCompactionMessage(new Date(initialComposerStateSince || Date.now()));"
      assert_includes APP_JAVASCRIPT, "composerState.textContent = `${waitingForOutputLabel} ${formatWaitDuration(elapsed)}`;"
      assert_includes APP_JAVASCRIPT, "if (event.type === \"compaction_start\") {\n      liveMessageRenderer.resetLiveCompactionTracking();\n      liveMessageRenderer.removePendingCompactionMessage();\n      liveMessageRenderer.appendPendingCompactionMessage(eventTimestamp(event));\n      setComposerState(\"running\", \"Compacting…\", { since: eventTimeMilliseconds(event) });\n    }"
    end
  end

  def test_initializes_composer_busy_state_for_running_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      client = FakeRpcClient.new([])
      def client.busy? = true
      def client.busy_since = Time.at(1_000)
      def client.agent_running? = true
      registry.register(path, client)
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "data-composer-state=\"running\""
      assert_includes response.body, "data-composer-state-since=\"1000000\""
      assert_includes response.body, "data-agent-running=\"true\""
      assert_includes APP_JAVASCRIPT, "const initialComposerState = liveOutput.dataset.composerState;"
      assert_includes APP_JAVASCRIPT, "const initialComposerStateSince = Number(liveOutput.dataset.composerStateSince || 0);"
      assert_includes APP_JAVASCRIPT, "liveAgentRunning = liveOutput.dataset.agentRunning === \"true\";"
      assert_includes APP_JAVASCRIPT, "setComposerState(initialComposerState, initialComposerLabel, { since: initialComposerStateSince, focus: false });"
      assert_includes APP_JAVASCRIPT, "if (state === \"running\" && (since || !waitingForOutputSince)) startWaitingForOutput(since || Date.now());"
      assert_includes APP_JAVASCRIPT, "payload.events.length > 0 && composerState?.dataset.state === \"running\" && !waitingForOutputSince"
    end
  end

  def test_conversation_scroll_exposes_older_history_metadata
    Dir.mktmpdir do |dir|
      messages = (1..180).map { |index| { role: "user", text: "Message #{index}" } }
      path = write_session_with_messages(dir, messages)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      scroll = document.at_css("#conversation-scroll")
      assert_equal "true", scroll["data-has-older-messages"]
      assert_equal "30", scroll["data-older-message-count"]
      assert_equal "30", scroll["data-older-message-cursor"]
      assert_includes scroll["data-older-messages-url"], "/conversation_older"
      status = scroll.at_css("button[data-conversation-history-status]")
      assert_equal "Earlier messages available", status.text.strip
      assert_includes APP_STYLESHEET, ".conversation-history-status"
      assert_includes APP_JAVASCRIPT, "finishHistoryStatus()"
      assert_includes APP_JAVASCRIPT, "failHistoryStatus()"
      assert_includes APP_JAVASCRIPT, "loadOlderHistory()"
      assert_includes APP_JAVASCRIPT, "previousHeight"
      assert_includes APP_JAVASCRIPT, "this.element.scrollTop = previousTop + (this.element.scrollHeight - previousHeight)"
    end
  end

  def test_serves_older_conversation_window_as_json
    Dir.mktmpdir do |dir|
      messages = (1..180).map { |index| { role: "user", text: "Message #{index}" } }
      path = write_session_with_messages(dir, messages)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/conversation_older",
        params: { "session" => path, "cursor" => "30" }
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.media_type
      payload = JSON.parse(response.body)
      assert_equal 0, payload.fetch("next_cursor")
      refute payload.fetch("has_older_messages")
      assert_includes payload.fetch("html"), "Message 1"
      assert_includes payload.fetch("html"), "Message 30"
      refute_includes payload.fetch("html"), "Message 31"
    end
  end

  def test_serves_all_remaining_conversation_messages_for_explicit_full_history_load
    Dir.mktmpdir do |dir|
      messages = (1..220).map { |index| { role: "user", text: "Message #{index}" } }
      path = write_session_with_messages(dir, messages)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/conversation_older",
        params: { "session" => path, "cursor" => "170", "all" => "1" }
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal 0, payload.fetch("next_cursor")
      refute payload.fetch("has_older_messages")
      assert_includes payload.fetch("html"), "Message 1"
      assert_includes payload.fetch("html"), "Message 170"
      refute_includes payload.fetch("html"), "Message 171"
    end
  end

  def test_does_not_render_large_raw_details_in_initial_conversation
    Dir.mktmpdir do |dir|
      large_raw_details = "large raw detail token #{"x" * 9000}"
      path = write_session_with_raw_messages(dir, [
        { type: "compaction", timestamp: "2026-06-13T10:00:00Z", summary: "Compacted", large: large_raw_details }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      refute document.at_css("details.raw-details")
      refute_includes response.body, large_raw_details
    end
  end

  def test_does_not_render_small_raw_details_inline
    Dir.mktmpdir do |dir|
      small_raw_details = "small raw detail token"
      path = write_session_with_raw_messages(dir, [
        { type: "compaction", timestamp: "2026-06-13T10:00:00Z", summary: "Compacted", small: small_raw_details }
      ])
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      refute document.at_css("details.raw-details")
      refute_includes response.body, small_raw_details
    end
  end

  def test_older_conversation_window_does_not_render_raw_details
    Dir.mktmpdir do |dir|
      large_raw_details = "older raw detail token #{"x" * 9000}"
      raw_entries = Array.new(10) do |index|
        { type: "message", timestamp: "2026-06-13T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "Message #{index + 1}" }] } }
      end
      raw_entries << { type: "compaction", timestamp: "2026-06-13T10:10:00Z", summary: "Compacted", large: large_raw_details }
      path = write_session_with_raw_messages(dir, raw_entries)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/conversation_older",
        params: { "session" => path, "cursor" => "11" }
      )

      assert_equal 200, response.status
      html = JSON.parse(response.body).fetch("html")
      document = Nokogiri::HTML(html)
      refute document.at_css("details.raw-details")
      refute_includes html, large_raw_details
    end
  end

  def test_raw_details_endpoint_is_not_exposed
    response = Rack::MockRequest.new(PiWebGateway).get("/message_raw_details")

    assert_equal 404, response.status
  end

  def test_older_conversation_window_handles_missing_session_silently
    Dir.mktmpdir do |dir|
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/conversation_older",
        params: { "session" => File.join(dir, "missing.jsonl"), "cursor" => "30" }
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_equal "", payload.fetch("html")
      assert_equal 0, payload.fetch("next_cursor")
      refute payload.fetch("has_older_messages")
      assert_equal 0, payload.fetch("older_message_count")
    end
  end

  def test_older_conversation_window_rejects_session_outside_root
    Dir.mktmpdir do |dir|
      Dir.mktmpdir do |outside_dir|
        outside_path = write_session_with_messages(outside_dir, [{ role: "user", text: "Outside message" }])
        PiWebGateway.set :sessions_root, dir
        PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

        response = Rack::MockRequest.new(PiWebGateway).get(
          "/conversation_older",
          params: { "session" => outside_path, "cursor" => "1" }
        )

        assert_equal 200, response.status
        payload = JSON.parse(response.body)
        assert_equal "", payload.fetch("html")
        refute payload.fetch("has_older_messages")
      end
    end
  end

  def test_renders_visual_polish_affordances
    Dir.mktmpdir do |dir|
      path = write_session_with_messages(dir, [{ role: "assistant", text: "Copy me" }])
      PiWebGateway.set :sessions_root, dir
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { FakeRpcClient.new([]) })
      registry.register(path, FakeRpcClient.new([]))
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes APP_STYLESHEET, "color-scheme: dark"
      assert_includes APP_STYLESHEET, "session-running-indicator"
      refute_includes response.body, ">active</span>"
      assert_includes response.body, "copy-button"
      assert_includes APP_JAVASCRIPT, "code-block-copy-button"
      assert_includes response.body, "data-copy-target"
      assert_includes APP_JAVASCRIPT, "enhanceMarkdownCodeBlocks(job.body, this.document)"
      assert_includes APP_JAVASCRIPT, 'button.dataset.copyTarget === "code-block"'
      assert_includes APP_JAVASCRIPT, "window.piGatewayElectron?.copyText"
      assert_includes APP_JAVASCRIPT, "navigator.clipboard.writeText"
      assert_includes APP_JAVASCRIPT, "window.isSecureContext"
      assert_includes APP_JAVASCRIPT, "catch (_error)"
      assert_includes APP_JAVASCRIPT, "document.execCommand(\"copy\")"
      assert_includes APP_JAVASCRIPT, "Copy failed"
      assert_includes APP_STYLESHEET, "empty-state"
      assert_includes APP_STYLESHEET, "button:hover"
    end
  end

  def test_workspace_access_store_tracks_approved_pending_and_denied_workspaces
    path = File.join(@workspace_root, "workspace-access.json")
    store = WorkspaceAccessStore.new(path: path)

    refute store.approved?("workspace-a")
    store.approve_workspace("workspace-a")
    assert store.approved?("workspace-a")

    request = store.request_access("workspace-b", browser_token: "browser-token")
    assert_match(/\A[A-Z0-9]{4}-[A-Z0-9]{4}\z/, request.fetch("code"))
    assert_equal "pending", store.pending_status("workspace-b")
    assert_equal request.fetch("code"), store.pending_requests.first.fetch("code")

    store.deny_code(request.fetch("code"))
    assert_equal "denied", store.pending_status("workspace-b")
    assert_empty store.pending_requests

    stored = JSON.parse(File.read(path))
    assert_equal ["workspace-a"], stored.fetch("approved_workspaces").map { |workspace| workspace.fetch("workspace_id") }
    assert_equal ["workspace-b"], stored.fetch("pending_requests").map { |pending| pending.fetch("workspace_id") }
  end

  def test_browser_auth_disabled_still_requires_user_token_in_multi_user_mode
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :browser_auth_disabled, true
      PiWebGateway.set :multi_user_mode, true

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 403, response.status
      assert_includes response.body, "User token"
      refute_includes response.body, "Browser access required"
    end
  end

  def test_browser_auth_disabled_auto_approves_new_user_token_in_multi_user_mode
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, nil
      PiWebGateway.set :browser_auth_disabled, true
      PiWebGateway.set :multi_user_mode, true
      request = Rack::MockRequest.new(PiWebGateway)

      response = request.post("/workspace-key", params: { "workspace_key" => "piu_correct_horse_42" })

      workspace_cookie = Array(response["Set-Cookie"]).first.split(";", 2).first
      assert_equal 303, response.status
      assert_includes workspace_cookie, "pi_gateway_workspace="
      assert WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path).approved?(workspace_id_from_cookie(workspace_cookie))
    end
  end

  def test_multi_user_flow_uses_user_token_without_browser_approval
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      request = Rack::MockRequest.new(PiWebGateway)

      blocked = request.get("/")
      assert_equal 403, blocked.status
      assert_includes blocked.body, "User token"
      refute_includes blocked.body, "Browser access required"
      assert_includes blocked.body, "First time here? Generate your token"
      assert_includes blocked.body, "Admin password"

      weak_token = request.post("/workspace-key", params: { "workspace_key" => "short" })
      assert_equal 403, weak_token.status
      assert_includes weak_token.body, "Enter a valid user token"

      wrong_admin_password = request.post(
        "/workspace-key",
        params: { "workspace_key" => "piu_correct_horse_42", "admin_password" => "wrong" }
      )
      assert_equal 403, wrong_admin_password.status
      assert_includes wrong_admin_password.body, "Admin password did not match"
      assert_includes wrong_admin_password.body, PiWebGateway.settings.workspace_access_path
      assert_includes wrong_admin_password.body, "pending request"
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      assert_equal "pending", store.pending_status(workspace_id_for("piu_correct_horse_42"))

      key_response = request.post(
        "/workspace-key",
        params: { "workspace_key" => "piu_correct_horse_42", "admin_password" => "secret" }
      )
      workspace_cookie = Array(key_response["Set-Cookie"]).first.split(";", 2).first
      assert_equal 303, key_response.status
      assert_includes workspace_cookie, "pi_gateway_workspace="
      assert WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path).approved?(workspace_id_from_cookie(workspace_cookie))
      assert File.exist?(PiWebGateway.settings.workspace_secret_path)
    end
  end

  def test_multi_user_approved_user_token_opens_immediately_without_browser_approval
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approved_workspace_id = workspace_id_for("piu_correct_horse_42")
      WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path).approve_workspace(approved_workspace_id)
      request = Rack::MockRequest.new(PiWebGateway)

      response = request.post("/workspace-key", params: { "workspace_key" => "piu_correct_horse_42" })

      workspace_cookie = Array(response["Set-Cookie"]).first.split(";", 2).first
      assert_equal 303, response.status
      assert_includes workspace_cookie, "pi_gateway_workspace=#{approved_workspace_id}"
    end
  end

  def test_multi_user_unknown_user_token_waits_for_approval_without_browser_approval
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approved_workspace_id = workspace_id_for("piu_correct_horse_42")
      WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path).approve_workspace(approved_workspace_id)
      request = Rack::MockRequest.new(PiWebGateway)

      response = request.post("/workspace-key", params: { "workspace_key" => "piu_different_horse_42" })

      assert_equal 403, response.status
      assert_includes response.body, "Waiting for workspace approval"
      refute_includes response.body, "Admin password"
      refute_includes Array(response["Set-Cookie"]).join("\n"), "pi_gateway_workspace="
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      assert_equal "pending", store.pending_status(workspace_id_for("piu_different_horse_42"))
    end
  end

  def test_multi_user_generates_user_token_once_for_copying
    PiWebGateway.set :gateway_admin_password, "secret"
    PiWebGateway.set :multi_user_mode, true

    response = Rack::MockRequest.new(PiWebGateway).post("/workspace-token/generate")

    assert_equal 200, response.status
    assert_match(/piu_[A-Za-z0-9_-]{43}/, response.body)
    assert_includes response.body, "Copy token"
    assert_includes response.body, "recovered"
    assert_includes response.body, "sensitive and private"
  end

  def test_approved_workspace_can_approve_pending_workspace_request
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approver_cookie = workspace_cookie_for("Correct Horse 42")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("approver", label: "test")
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      pending = store.request_access(workspace_id_for("Different Horse 42"), browser_token: "requester")
      request = Rack::MockRequest.new(PiWebGateway)

      pending_response = request.get("/workspace-access/pending", "HTTP_COOKIE" => "pi_gateway_browser=approver; #{approver_cookie}")
      assert_equal 200, pending_response.status
      pending_payload = JSON.parse(pending_response.body).fetch("requests").first
      assert_equal pending.fetch("code"), pending_payload.fetch("code")
      refute_includes pending_payload.keys, "workspace_id"
      refute_includes pending_payload.keys, "browser_token"

      approve_response = request.post(
        "/workspace-access/approve",
        params: { "code" => pending.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=approver; #{approver_cookie}"
      )
      assert_equal 200, approve_response.status
      assert store.approved?(workspace_id_for("Different Horse 42"))
    end
  end

  def test_approved_workspace_status_cookie_is_only_set_for_requesting_browser
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approver_cookie = workspace_cookie_for("Correct Horse 42")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("approver", label: "test")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("requester", label: "test")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("other", label: "test")
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      pending = store.request_access(workspace_id_for("Different Horse 42"), browser_token: "requester")
      request = Rack::MockRequest.new(PiWebGateway)
      request.post(
        "/workspace-access/approve",
        params: { "code" => pending.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=approver; #{approver_cookie}"
      )

      other_status = request.get("/workspace-access/status", params: { "code" => pending.fetch("code") }, "HTTP_COOKIE" => "pi_gateway_browser=other")
      requester_status = request.get("/workspace-access/status", params: { "code" => pending.fetch("code") }, "HTTP_COOKIE" => "pi_gateway_browser=requester")

      assert_equal 200, other_status.status
      assert_equal "approved", JSON.parse(other_status.body).fetch("status")
      refute_includes Array(other_status["Set-Cookie"]).join("\n"), "pi_gateway_workspace="
      assert_equal 200, requester_status.status
      assert_includes Array(requester_status["Set-Cookie"]).join("\n"), "pi_gateway_workspace="
    end
  end

  def test_repeated_workspace_requests_are_bound_to_each_requesting_browser
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approver_cookie = workspace_cookie_for("Correct Horse 42")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("approver", label: "test")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("first", label: "test")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("second", label: "test")
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      workspace_id = workspace_id_for("Different Horse 42")
      first_request = store.request_access(workspace_id, browser_token: "first")
      second_request = store.request_access(workspace_id, browser_token: "second")
      request = Rack::MockRequest.new(PiWebGateway)
      request.post(
        "/workspace-access/approve",
        params: { "code" => second_request.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=approver; #{approver_cookie}"
      )

      first_status = request.get("/workspace-access/status", params: { "code" => first_request.fetch("code") }, "HTTP_COOKIE" => "pi_gateway_browser=first")
      second_status = request.get("/workspace-access/status", params: { "code" => second_request.fetch("code") }, "HTTP_COOKIE" => "pi_gateway_browser=second")

      refute_equal first_request.fetch("code"), second_request.fetch("code")
      assert_includes Array(first_status["Set-Cookie"]).join("\n"), "pi_gateway_workspace="
      assert_includes Array(second_status["Set-Cookie"]).join("\n"), "pi_gateway_workspace="
    end
  end

  def test_workspace_approval_requires_current_approved_workspace
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      workspace_cookie_for("Correct Horse 42")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("approved-browser", label: "test")
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      pending = store.request_access(workspace_id_for("Different Horse 42"), browser_token: "requester")
      request = Rack::MockRequest.new(PiWebGateway)

      pending_response = request.get("/workspace-access/pending", "HTTP_COOKIE" => "pi_gateway_browser=approved-browser")
      approve_response = request.post(
        "/workspace-access/approve",
        params: { "code" => pending.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=approved-browser"
      )

      assert_equal 403, pending_response.status
      assert_equal 403, approve_response.status
      refute store.approved?(workspace_id_for("Different Horse 42"))
    end
  end

  def test_approved_workspace_can_deny_pending_workspace_request
    Dir.mktmpdir do |dir|
      write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :gateway_admin_password, "secret"
      PiWebGateway.set :multi_user_mode, true
      approver_cookie = workspace_cookie_for("Correct Horse 42")
      BrowserAccessStore.new(path: PiWebGateway.settings.browser_access_path).approve_current_browser("approver", label: "test")
      store = WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path)
      workspace_id = workspace_id_for("Different Horse 42")
      pending = store.request_access(workspace_id, browser_token: "requester")
      request = Rack::MockRequest.new(PiWebGateway)

      deny_response = request.post(
        "/workspace-access/deny",
        params: { "code" => pending.fetch("code") },
        "HTTP_COOKIE" => "pi_gateway_browser=approver; #{approver_cookie}"
      )

      assert_equal 200, deny_response.status
      assert_equal "denied", store.pending_status(workspace_id)
    end
  end

  def test_multi_user_session_list_hides_unowned_and_other_workspace_sessions
    Dir.mktmpdir do |dir|
      own_path, other_path, unowned_path = write_sessions(dir, count: 3)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :multi_user_mode, true
      own_cookie = workspace_cookie_for("Correct Horse 42")
      other_workspace_id = workspace_id_for("Different Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(own_path, workspace_id_from_cookie(own_cookie))
      store.claim(other_path, other_workspace_id)

      response = Rack::MockRequest.new(PiWebGateway).get("/", "HTTP_COOKIE" => own_cookie)

      assert_equal 200, response.status
      assert_includes response.body, Rack::Utils.escape(own_path)
      refute_includes response.body, Rack::Utils.escape(other_path)
      refute_includes response.body, Rack::Utils.escape(unowned_path)
    end
  end

  def test_multi_user_mode_off_shows_sessions_even_if_ownership_file_exists
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :multi_user_mode, false
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(paths.first, "workspace-a")
      store.claim(paths.last, "workspace-b")

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 200, response.status
      assert_includes response.body, Rack::Utils.escape(paths.first)
      assert_includes response.body, Rack::Utils.escape(paths.last)
    end
  end

  def test_model_settings_returns_current_rpc_state_and_available_models
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, FakeRpcClient.new(calls, [], path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).get("/sessions/model_settings", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal({
        "state" => {
          "sessionFile" => path,
          "model" => { "provider" => "anthropic", "id" => "claude-sonnet-4" },
          "thinkingLevel" => "medium"
        },
        "models" => [{ "provider" => "anthropic", "id" => "claude-sonnet-4", "name" => "Claude Sonnet 4" }]
      }, JSON.parse(response.body))
      assert_equal [[:get_state], [:get_available_models]], calls
    end
  end

  def test_applies_model_before_thinking_level_when_session_is_idle
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, FakeRpcClient.new(calls))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/model_settings",
        params: { "session" => path, "provider" => "openai", "model" => "gpt-5", "thinking" => "high" }
      )

      assert_equal 200, response.status
      assert_equal({
        "model" => { "provider" => "openai", "id" => "gpt-5" },
        "thinking" => "high"
      }, JSON.parse(response.body))
      assert_equal [[:set_model, "openai", "gpt-5"], [:set_thinking_level, "high"], [:get_state]], calls
    end
  end

  def test_rejects_invalid_thinking_level_before_rpc
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, FakeRpcClient.new(calls))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/model_settings",
        params: { "session" => path, "provider" => "openai", "model" => "gpt-5", "thinking" => "unknown" }
      )

      assert_equal 400, response.status
      assert_empty calls
    end
  end

  def test_does_not_set_thinking_when_model_change_fails
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      client = FakeRpcClient.new(calls)
      def client.set_model(provider, model_id)
        @calls << [:set_model, provider, model_id]
        { "success" => false, "error" => "Model not found" }
      end
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/model_settings",
        params: { "session" => path, "provider" => "invalid", "model" => "missing", "thinking" => "high" }
      )

      assert_equal 422, response.status
      assert_equal({ "success" => false, "error" => "Model not found" }, JSON.parse(response.body))
      assert_equal [[:set_model, "invalid", "missing"]], calls
    end
  end

  def test_returns_rpc_error_for_malformed_model_setting_response
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      client = FakeRpcClient.new([])
      def client.set_model(_provider, _model_id) = nil
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/model_settings",
        params: { "session" => path, "provider" => "openai", "model" => "gpt-5", "thinking" => "high" }
      )

      assert_equal 422, response.status
      assert_equal "Setting could not be changed", JSON.parse(response.body).fetch("error")
    end
  end

  def test_rejects_model_settings_changes_while_session_is_busy
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      client = FakeRpcClient.new(calls)
      def client.busy? = true
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/sessions/model_settings",
        params: { "session" => path, "provider" => "openai", "model" => "gpt-5", "thinking" => "high" }
      )

      assert_equal 409, response.status
      assert_equal({ "error" => "Session is busy" }, JSON.parse(response.body))
      assert_empty calls
    end
  end

  def test_cycles_thinking_level_when_session_is_idle
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, FakeRpcClient.new(calls))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post("/sessions/cycle_thinking", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal({ "thinking" => "high" }, JSON.parse(response.body))
      assert_equal [[:cycle_thinking_level]], calls
    end
  end

  def test_returns_nil_when_thinking_cycle_is_unsupported
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      client = FakeRpcClient.new(calls)
      def client.cycle_thinking_level
        @calls << [:cycle_thinking_level]
        { "success" => true, "data" => nil }
      end
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post("/sessions/cycle_thinking", params: { "session" => path })

      assert_equal 200, response.status
      assert_equal({ "thinking" => nil }, JSON.parse(response.body))
      assert_equal [[:cycle_thinking_level]], calls
    end
  end

  def test_rejects_thinking_cycle_while_session_is_busy
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      client = FakeRpcClient.new(calls)
      def client.busy? = true
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(path, client)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry

      response = Rack::MockRequest.new(PiWebGateway).post("/sessions/cycle_thinking", params: { "session" => path })

      assert_equal 409, response.status
      assert_equal({ "error" => "Session is busy" }, JSON.parse(response.body))
      assert_empty calls
    end
  end

  def test_multi_user_direct_session_endpoints_reject_other_workspace_sessions
    Dir.mktmpdir do |dir|
      own_path, other_path = write_sessions(dir, count: 2)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :multi_user_mode, true
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls)
      }]
      own_cookie = workspace_cookie_for("Correct Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(own_path, workspace_id_from_cookie(own_cookie))
      store.claim(other_path, workspace_id_for("Different Horse 42"))

      status_response = Rack::MockRequest.new(PiWebGateway).get("/status", params: { "session" => other_path }, "HTTP_COOKIE" => own_cookie)
      prompt_response = Rack::MockRequest.new(PiWebGateway).post("/prompt", params: { "session" => other_path, "message" => "Hello" }, "HTTP_COOKIE" => own_cookie)
      model_settings_response = Rack::MockRequest.new(PiWebGateway).get("/sessions/model_settings", params: { "session" => other_path }, "HTTP_COOKIE" => own_cookie)
      apply_model_response = Rack::MockRequest.new(PiWebGateway).post("/sessions/model_settings", params: { "session" => other_path, "provider" => "openai", "model" => "gpt-5", "thinking" => "high" }, "HTTP_COOKIE" => own_cookie)
      cycle_thinking_response = Rack::MockRequest.new(PiWebGateway).post("/sessions/cycle_thinking", params: { "session" => other_path }, "HTTP_COOKIE" => own_cookie)

      assert_equal 404, status_response.status
      assert_equal 404, prompt_response.status
      assert_equal 404, model_settings_response.status
      assert_equal 404, apply_model_response.status
      assert_equal 404, cycle_thinking_response.status
      assert_empty calls
    end
  end

  def test_multi_user_rejects_other_workspace_attachment_urls
    Dir.mktmpdir do |dir|
      own_path, other_path = write_sessions(dir, count: 2)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :multi_user_mode, true
      cookie = workspace_cookie_for("Correct Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(own_path, workspace_id_from_cookie(cookie))
      store.claim(other_path, workspace_id_for("Different Horse 42"))
      other_hash = Digest::SHA256.hexdigest(other_path)
      attachment_dir = File.join(PiWebGateway.settings.attachments_root, other_hash)
      FileUtils.mkdir_p(attachment_dir)
      File.binwrite(File.join(attachment_dir, "#{"a" * 64}.png"), "image")

      response = Rack::MockRequest.new(PiWebGateway).get("/attachments/#{other_hash}/#{"a" * 64}.png", "HTTP_COOKIE" => cookie)

      assert_equal 404, response.status
    end
  end

  def test_multi_user_sidebar_hides_other_workspace_active_pending_session
    Dir.mktmpdir do |dir|
      own_path = write_session(dir)
      own_pending_path = File.join(dir, "own-pending-session.jsonl")
      other_pending_path = File.join(dir, "other-pending-session.jsonl")
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(own_pending_path, FakeRpcClient.new([]))
      registry.register(other_pending_path, FakeRpcClient.new([]))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(
        own_pending_path => project_cwd(dir),
        other_pending_path => project_cwd(dir)
      )
      PiWebGateway.set :multi_user_mode, true
      own_cookie = workspace_cookie_for("Correct Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(own_path, workspace_id_from_cookie(own_cookie))
      store.claim(own_pending_path, workspace_id_from_cookie(own_cookie))
      store.claim(other_pending_path, workspace_id_for("Different Horse 42"))

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => own_path }, "HTTP_COOKIE" => own_cookie)

      assert_equal 200, response.status
      assert_includes response.body, own_path
      assert_includes response.body, own_pending_path
      refute_includes response.body, other_pending_path
    end
  end

  def test_multi_user_rejects_other_workspace_pending_session_without_remapping
    Dir.mktmpdir do |dir|
      real_path = write_session(dir)
      pending_path = File.join(dir, "pending-session.jsonl")
      calls = []
      registry = PiRpcClientRegistry.new(factory: ->(_session_path) { raise "unexpected start" })
      registry.register(pending_path, FakeRpcClient.new(calls, [], real_path))
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_registry, registry
      PiWebGateway.set :pending_session_registry, Rpc::PendingSessionRegistry.new(pending_path => project_cwd(dir))
      PiWebGateway.set :multi_user_mode, true
      own_cookie = workspace_cookie_for("Correct Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(pending_path, workspace_id_for("Different Horse 42"))

      response = Rack::MockRequest.new(PiWebGateway).post("/prompt", params: { "session" => pending_path, "message" => "Hello" }, "HTTP_COOKIE" => own_cookie)

      assert_equal 404, response.status
      assert_empty calls
      assert registry.active?(pending_path)
      refute registry.active?(real_path)
      assert_includes PiWebGateway.pending_session_registry.paths, pending_path
    end
  end

  def test_multi_user_new_session_claims_workspace_owner
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      new_path = File.join(File.dirname(path), "new-session.jsonl")
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :multi_user_mode, true
      PiWebGateway.set :new_rpc_client_factory, [->(cwd) {
        calls << [:start_new, cwd]
        FakeRpcClient.new(calls, [], new_path)
      }]
      cookie = workspace_cookie_for("Correct Horse 42")
      store = WorkspaceSessionOwnershipStore.new(path: PiWebGateway.settings.workspace_ownership_path)
      store.claim(path, workspace_id_from_cookie(cookie))

      response = Rack::MockRequest.new(PiWebGateway).post("/sessions/new", params: { "session" => path }, "HTTP_COOKIE" => cookie)

      assert_equal 303, response.status
      assert store.owned_by?(new_path, workspace_id_from_cookie(cookie))
    end
  end

  private

  def assert_event_payload(response, events:, last_seq:, mode:)
    payload = JSON.parse(response.body)
    sync = payload.delete("session_sync")
    assert_equal({ "events" => events, "last_seq" => last_seq, "missed" => false }, payload)
    assert_equal mode, sync.fetch("mode")
    refute_empty sync.fetch("revision")
  end

  def compact_card_with_summary(document, summary)
    document.css(".message--compact").find do |card|
      card.at_css(".compact-summary")&.text == summary
    end
  end

  class SyncAwareRpcClient
    attr_accessor :busy, :events, :settled_at

    def initialize(known_entries, leaf_id, calls)
      @known_entries = known_entries
      @leaf_id = leaf_id
      @calls = calls
      @busy = false
      @events = []
    end

    def session_position(append_cursor)
      { known: append_cursor.nil? || @known_entries.include?(append_cursor), leaf_id: @leaf_id, error: nil }
    end

    def session_entries_after(append_cursor)
      position = session_position(append_cursor)
      index = append_cursor.nil? ? -1 : @known_entries.index(append_cursor)
      entries = position[:known] ? @known_entries.drop(index + 1).map { |id| { "id" => id } } : []
      position.merge(entries: entries)
    end

    def prompt(message, _images = [])
      @calls << [:prompt, message]
      { "success" => true }
    end

    def busy?
      @busy
    end

    def events_after(after_seq)
      visible_events = after_seq.to_i.zero? ? @events : []
      { events: visible_events, last_seq: @events.length, missed: false }
    end

    def close
      @calls << [:close]
    end
  end

  class FakeRpcClient
    def initialize(calls, events_or_commands = [], session_file = nil)
      @calls = calls
      @events = events_or_commands
      @commands = events_or_commands
      @session_file = session_file
      @model = { "provider" => "anthropic", "id" => "claude-sonnet-4" }
      @thinking_level = "medium"
    end

    def prompt(message, images = [])
      @calls << (images.empty? ? [:prompt, message] : [:prompt, message, images])
    end

    def steer(message)
      @calls << [:steer, message]
    end

    def follow_up(message, images = [])
      @calls << (images.empty? ? [:follow_up, message] : [:follow_up, message, images])
    end

    def get_messages
      @calls << [:get_messages]
    end

    def new_session(parent_session = nil)
      @calls << [:new_session, parent_session]
      { "type" => "response", "command" => "new_session", "success" => true, "data" => { "cancelled" => false } }
    end

    def get_state
      @calls << [:get_state]
      {
        "type" => "response",
        "command" => "get_state",
        "success" => true,
        "data" => {
          "sessionFile" => @session_file,
          "model" => @model,
          "thinkingLevel" => @thinking_level
        }
      }
    end

    def session_position(_append_cursor)
      { known: true, leaf_id: @session_file, error: nil }
    end

    def session_entries_after(append_cursor)
      session_position(append_cursor).merge(entries: [])
    end

    def get_available_models
      @calls << [:get_available_models]
      {
        "type" => "response",
        "command" => "get_available_models",
        "success" => true,
        "data" => { "models" => [{ "provider" => "anthropic", "id" => "claude-sonnet-4", "name" => "Claude Sonnet 4" }] }
      }
    end

    def set_model(provider, model_id)
      @calls << [:set_model, provider, model_id]
      @model = { "provider" => provider, "id" => model_id }
      { "success" => true }
    end

    def set_thinking_level(level)
      @calls << [:set_thinking_level, level]
      @thinking_level = level
      { "success" => true }
    end

    def cycle_thinking_level
      @calls << [:cycle_thinking_level]
      { "type" => "response", "command" => "cycle_thinking_level", "success" => true, "data" => { "level" => "high" } }
    end

    def get_commands
      @calls << [:get_commands]
      { "type" => "response", "command" => "get_commands", "success" => true, "data" => { "commands" => @commands } }
    end

    def get_fork_messages
      @calls << [:get_fork_messages]
      { "type" => "response", "command" => "get_fork_messages", "success" => true, "data" => { "messages" => @commands } }
    end

    def fork(entry_id)
      @calls << [:fork, entry_id]
      { "type" => "response", "command" => "fork", "success" => true, "data" => { "text" => "Forked prompt", "cancelled" => false } }
    end

    def clone_session
      @calls << [:clone_session]
      { "type" => "response", "command" => "clone", "success" => true, "data" => { "cancelled" => false } }
    end

    def navigate_tree(entry_id)
      @calls << [:navigate_tree, entry_id]
      @session_file = entry_id
      { "type" => "response", "command" => "prompt", "success" => true, "data" => { "cancelled" => false } }
    end

    def tree_leaf
      @calls << [:tree_leaf]
      @session_file
    end

    def abort
      @calls << [:abort]
    end

    def compact(instructions = nil)
      @calls << [:compact, instructions]
    end

    def set_session_name(name)
      @calls << [:set_session_name, name]
    end

    def event_sequence
      @events.length
    end

    def events_after(after_seq)
      @calls << [:events_after, after_seq]
      events = after_seq.to_i.zero? ? @events : []
      { events: events, last_seq: @events.length, missed: false }
    end

    def close
      @calls << [:close]
    end
  end

  def write_session(root)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    FileUtils.mkdir_p(project_cwd(root))
    path = File.join(session_dir, "session.jsonl")
    File.write(path, JSON.generate({ type: "session", id: "session-1", timestamp: Time.now.utc.iso8601(3), cwd: project_cwd(root) }) + "\n")
    path
  end

  def write_sessions(root, count:)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    FileUtils.mkdir_p(project_cwd(root))

    (1..count).map do |index|
      path = File.join(session_dir, "session-#{index}.jsonl")
      File.write(path, [
        JSON.generate({ type: "session", id: "session-#{index}", timestamp: Time.at(index).utc.iso8601(3), cwd: project_cwd(root) }),
        JSON.generate({ type: "session_info", name: "Session #{index}" })
      ].join("\n") + "\n")
      path
    end
  end

  def write_session_with_messages(root, messages)
    entries = messages.map.with_index do |message, index|
      {
        type: "message",
        timestamp: "2026-06-13T10:0#{index}:00Z",
        message: { role: message.fetch(:role), content: [{ type: "text", text: message.fetch(:text) }] }
      }
    end
    write_session_with_raw_messages(root, entries)
  end

  def write_session_with_raw_messages(root, messages)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    FileUtils.mkdir_p(project_cwd(root))
    path = File.join(session_dir, "messages.jsonl")
    entries = [{ type: "session", id: "session-1", cwd: project_cwd(root) }] + messages
    File.write(path, entries.map { |entry| JSON.generate(entry) }.join("\n") + "\n")
    path
  end

  def workspace_cookie_for(key)
    workspace_id = workspace_id_for(key)
    WorkspaceAccessStore.new(path: PiWebGateway.settings.workspace_access_path).approve_workspace(workspace_id)
    "pi_gateway_workspace=#{workspace_id}"
  end

  def workspace_id_for(key)
    secret = WorkspaceSecretStore.new(path: PiWebGateway.settings.workspace_secret_path).secret
    OpenSSL::HMAC.hexdigest("SHA256", secret, key.strip)
  end

  def workspace_id_from_cookie(cookie)
    cookie.split("=", 2).last
  end

  def with_env(values)
    previous = values.to_h { |key, _value| [key, ENV[key]] }
    values.each do |key, value|
      if value.nil?
        ENV.delete(key)
      else
        ENV[key] = value
      end
    end
    yield
  ensure
    previous.each do |key, value|
      if value.nil?
        ENV.delete(key)
      else
        ENV[key] = value
      end
    end
  end

  def session_url_for(path)
    "/?session=#{Rack::Utils.escape(path)}"
  end

  def project_cwd(root)
    File.join(root, "project")
  end
end

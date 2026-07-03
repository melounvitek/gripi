ENV["PI_GATEWAY_ADMIN_PASSWORD"] ||= "test-password"

require "minitest/autorun"
require "rack/mock"
require "open3"
require "rbconfig"
require "tmpdir"
require "json"
require "fileutils"
require "base64"
require_relative "../app"

class AppTest < Minitest::Test
  def setup
    @attachments_root = Dir.mktmpdir
    @read_state_root = Dir.mktmpdir
    @browser_access_root = Dir.mktmpdir
    @workspace_root = Dir.mktmpdir
    PiWebGateway.set :attachments_root, @attachments_root
    PiWebGateway.set :read_state_path, File.join(@read_state_root, "read-state.json")
    PiWebGateway.set :browser_access_path, File.join(@browser_access_root, "browser-access.json")
    PiWebGateway.set :multi_user_mode, false
    PiWebGateway.set :workspace_secret_path, File.join(@workspace_root, "workspace-secret")
    PiWebGateway.set :workspace_access_path, File.join(@workspace_root, "workspace-access.json")
    PiWebGateway.set :workspace_ownership_path, File.join(@workspace_root, "session-owners.json")
    PiWebGateway.set :gateway_admin_password, nil
    PiWebGateway.set :session_cwds_path, nil
    PiWebGateway.set :rpc_client_registry, nil
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
      assert_includes response.body, 'href="/notification-test"'
      assert_includes response.body, "Notifications"
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

  def test_follow_up_prompt_treats_fork_tree_clone_and_new_slash_commands_as_messages
    ["/fork", "/tree", "/clone", "/new"].each do |message|
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
      assert_equal({ "events" => [{ "type" => "assistant_delta", "text" => "Hi" }], "last_seq" => 1, "missed" => false }, JSON.parse(response.body))
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
      assert_equal({ "events" => [{ "type" => "assistant_delta", "text" => "Hi" }], "last_seq" => 1, "missed" => false }, JSON.parse(second.body))
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
      assert_equal({ "events" => [], "last_seq" => 0, "missed" => false }, JSON.parse(response.body))
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

      assert_equal({ "events" => [{ "type" => "from-a" }], "last_seq" => 1, "missed" => false }, JSON.parse(response_a.body))
      assert_equal({ "events" => [{ "type" => "from-b" }], "last_seq" => 1, "missed" => false }, JSON.parse(response_b.body))
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
      refute_includes conversation_html, "Later prompt"
      refute_includes conversation_html, "Later answer"
      assert_equal 200, page_response.status
      page_conversation_text = Nokogiri::HTML(page_response.body).at_css(".conversation-panel").text
      assert_includes page_conversation_text, "First prompt"
      assert_includes page_conversation_text, "First answer"
      assert_includes page_conversation_text, "Viewing earlier tree point"
      refute_includes page_conversation_text, "Later prompt"
      refute_includes page_conversation_text, "Later answer"
      assert_equal [[:start, path], [:navigate_tree, "assistant-1"], [:tree_leaf], [:tree_leaf]], calls
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
      assert_equal({ "events" => [], "last_seq" => 0, "missed" => false }, JSON.parse(response.body))
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

  def test_deletes_sessions_whose_cwd_no_longer_exists
    Dir.mktmpdir do |dir|
      stale_dir = File.join(dir, "--stale--")
      FileUtils.mkdir_p(stale_dir)
      stale_path = File.join(stale_dir, "stale.jsonl")
      File.write(stale_path, JSON.generate({ type: "session", id: "stale", cwd: File.join(dir, "deleted-worktree") }) + "\n")
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/")

      assert_equal 200, response.status
      refute File.exist?(stale_path)
      assert_includes response.body, path
      refute_includes response.body, "stale.jsonl"
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
      assert_includes response.body, "Slash commands (6)"
      assert_includes response.body, "/review"
      assert_includes response.body, "Review code"
      assert_includes response.body, "/compact"
      assert_includes response.body, "/fork"
      assert_includes response.body, "/tree"
      assert_includes response.body, "/clone"
      assert_includes response.body, "/new"
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
      assert_includes response.body, "Slash commands (5)"
      assert_includes response.body, "/compact"
      assert_includes response.body, "/fork"
      assert_includes response.body, "/tree"
      assert_includes response.body, "/clone"
      assert_includes response.body, "/new"
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
      refute_includes response.body, "Thinking</span>"
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
      PiWebGateway.set :sessions_root, dir
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
      assert_includes response.body, ".composer-state { display: none; align-items: center;"
      refute_includes response.body, ".composer-state { position: absolute;"
      refute_includes response.body, "Ready"
      assert_includes response.body, "composer-input-row"
      assert_includes response.body, "Attach images"
      assert_includes response.body, "send-button"
      assert_includes response.body, "composer-stop-button"
      assert_includes response.body, "session-abort-button composer-stop-button"
      assert_includes response.body, "Loading…"
      refute_includes response.body, "Loading session…"
      assert_includes response.body, "Send follow-up…"
      assert_includes response.body, "[hidden] { display: none !important; }"
      assert_includes response.body, "Ask Pi… Enter to send, Shift+Enter for newline."
      refute_includes response.body, "autofocus"
      assert_includes response.body, "Abort running Pi"
      refute_includes response.body, "class=\"danger abort-button session-abort-button\" form=\"abort-form\""
      refute_includes response.body, "Optional compact instructions"
      refute_includes response.body, ">Compact</button>"
      assert_includes response.body, "nearConversationBottom"
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
      assert_includes response.body, "scrollbar-gutter: stable"
      assert_includes response.body, ".conversation-scroll { min-height: 0; overflow-y: auto; overflow-x: hidden;"
      assert_includes response.body, ".jump-controls { position: sticky; z-index: 3; display: flex;"
      assert_includes response.body, "min-height: 2rem; margin: 0.25rem auto; visibility: hidden; opacity: 0;"
      assert_includes response.body, ".jump-button { display: none; align-items: center; justify-content: center; width: 2.75rem; height: 2rem; min-height: 0; padding: 0;"
      assert_includes response.body, ".jump-controls.is-visible { visibility: visible; opacity: 1; }"
      assert_includes response.body, "body:not(.is-conversation-scrolling) .jump-controls.is-visible { visibility: hidden; opacity: 0; pointer-events: none; }"
      assert_includes response.body, "function updateConversationJumpControlsReveal()"
      assert_includes response.body, "conversationScrollRevealDelayTimer = setTimeout"
      assert_includes response.body, "Date.now() - lastConversationScrollRevealAt > 120"
      assert_includes response.body, "}, 300);"
      assert_includes response.body, "updateConversationJumpControlsReveal();"
      assert_includes response.body, 'conversationScrollDirection === "up" && !autoScrollEnabled && !nearConversationTop()'
      assert_includes response.body, 'conversationScrollDirection === "down" && !nearConversationBottom()'
      assert_includes response.body, ".message--tool .message-details-summary, .message--tool-transcript .message-details-summary { max-width: 100%; overflow-x: auto; white-space: nowrap; }"
      assert_includes response.body, ".message--compact .message-details-summary:last-child { margin-bottom: 0; }"
      assert_includes response.body, ".message--tool .message-body, .message--tool-transcript .message-body { max-width: 100%; overflow-x: auto; }"
      assert_includes response.body, ".message--tool-transcript .message-body { display: grid; grid-template-columns: minmax(100%, max-content); color: rgba(216, 222, 216, 0.62); font-size: 0.92rem; line-height: 1.35; tab-size: 2; white-space: pre; overflow-wrap: normal; word-break: normal; }"
      assert_includes response.body, ".tool-diff-line { display: block; margin: 0 -0.25rem;"
      assert_includes response.body, "scrollbar-width: none"
      assert_includes response.body, ".message--user { margin-left: 10%; background: #343541; border-color: rgba(69, 133, 255, 0.72); color: #d4d4d4; }"
      assert_includes response.body, ".message--assistant { margin-right: 10%; background: #080d20; border-color: rgba(69, 133, 255, 0.32); color: #f0c7a4; }"
      assert_includes response.body, ".message--thinking { margin-right: 16%; background: #080d20; border-color: rgba(69, 133, 255, 0.22); border-style: dashed; color: #7f7f88; box-shadow: none; }"
      assert_includes response.body, ".message--tool, .message--tool-call { background: rgba(28, 38, 32, 0.82); border-color: rgba(138, 190, 183, 0.24); border-style: dashed; color: rgba(216, 222, 216, 0.74); box-shadow: none; }"
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
        JSON.generate({ type: "session", id: "session-a", cwd: project_a }),
        JSON.generate({ type: "session_info", name: "Alpha work" })
      ].join("\n") + "\n")
      File.write(path_b, [
        JSON.generate({ type: "session", id: "session-b", cwd: project_b }),
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
      assert_equal "false", toggle["aria-expanded"]
      assert_equal "sidebar-session-search", toggle["aria-controls"]
      assert_equal "sidebar-session-search", search_form["id"]
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
      assert_equal 200, message_response.status
      assert_includes message_response.body, "Investigate webhook delivery"
      assert_equal 200, cwd_response.status
      assert_includes cwd_response.body, "beta-project"
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
      File.write(older_path, [
        JSON.generate({ type: "session", id: "older", cwd: older_cwd }),
        JSON.generate({ type: "session_info", name: "Older work" })
      ].join("\n") + "\n")
      File.write(newer_path, [
        JSON.generate({ type: "session", id: "newer", cwd: newer_cwd }),
        JSON.generate({ type: "session_info", name: "Newer work" })
      ].join("\n") + "\n")
      FileUtils.touch(older_path, mtime: Time.now - 60)
      FileUtils.touch(newer_path, mtime: Time.now)
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

  def test_sidebar_project_filter_keeps_current_and_unread_sessions_visible
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
      assert_equal ["Unread", "Sessions"], document.css(".recent-sessions-header h2").map(&:text)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal ["Selected work", "Unread work", "Filtered work"], session_titles
      assert document.at_css('.recent-sessions a.session[href*="selected.jsonl"]')["class"].include?("selected")
      assert document.at_css('.recent-sessions a.session[href*="unread.jsonl"]')["class"].include?("unread")
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

      FileUtils.touch(path, mtime: Time.now)
      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "updated just now"
      refute_includes response.body, "updated 20"
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

  def test_recent_sessions_include_new_session_modal
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path, "show_all_sessions" => "1" })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      button = document.at_css('.recent-sessions [data-modal-open="new-session-modal"]')
      assert button
      assert_equal "+", button.text.strip
      modal = document.at_css('body > [data-modal="new-session-modal"]')
      assert modal
      assert_equal "/sessions/new_at_cwd", modal.at_css('form.new-session-cwd-form')["action"]
      refute modal.at_css('input[name="show_all_sessions"]')
      refute modal.at_css('input[name="sidebar_sessions_limit"]')
      assert_includes modal.css('option').map { |option| option["value"] }, project_cwd(dir)
      project_option = modal.css('option').find { |option| option["value"] == project_cwd(dir) }
      assert_equal File.basename(project_cwd(dir)), project_option.text
      assert_includes modal.text, "Start session"
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
      File.write(older_path, JSON.generate({ type: "session", id: "older", cwd: older_cwd }) + "\n")
      File.write(newer_path, JSON.generate({ type: "session", id: "newer", cwd: newer_cwd }) + "\n")
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
      File.write(current_path, JSON.generate({ type: "session", id: "current", cwd: current_cwd }) + "\n")
      File.write(filtered_path, JSON.generate({ type: "session", id: "filtered", cwd: filtered_cwd }) + "\n")
      File.write(newer_path, JSON.generate({ type: "session", id: "newer", cwd: newer_cwd }) + "\n")
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
      assert_includes response.body, "function openModal(modal)"
      assert_includes response.body, "function closeModal(modal)"
      assert_includes response.body, "function modalIsOpen()"
      assert_includes response.body, ".session-switch-overlay { position: fixed; inset: 0; z-index: 140;"
      assert_includes response.body, ".modal-overlay { place-items: end stretch; padding: 0; }"
      assert_includes response.body, "submit.textContent = \"Starting…\""
      assert_includes response.body, "function replaceNewSessionModalHtml(html)"
      assert_includes response.body, "const [sidebarResponse, modalResponse] = await Promise.all(["
      assert_includes response.body, "fetch(newSessionModalUrl(targetUrl.href))"
      assert_includes response.body, "replaceNewSessionModalHtml(modalHtml);"
      assert_includes response.body, "replaceNewSessionModalHtml(payload.new_session_modal_html);"
      assert_includes response.body, "replaceForkSessionModalHtml(payload.fork_session_modal_html);"
      refute_includes response.body, "data-modal-open=\"fork-session-modal\""
      refute_includes response.body, "class=\"clone-session-form\""
      assert_includes response.body, "function loadForkMessages(modal)"
      assert_includes response.body, "fetch(\"/sessions/fork\", { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } })"
      assert_includes response.body, "await switchToBranchedSession(payload);"
      assert_includes response.body, "const originalForkText = forkOption.textContent;"
      assert_includes response.body, "forkOption.textContent = originalForkText;"
      assert_includes response.body, "showStatus(\"Could not fork this session\", true);"
      refute_includes response.body, "function makeForkButton(entryId)"
      refute_includes response.body, "function forkEntryIdFromEvent(event, message)"
      refute_includes response.body, "function scheduleResolveForkButton(entry, text)"
      assert_includes response.body, "abortEventPoll();"
      assert_includes response.body, "async function submitAbort(event)"
      assert_includes response.body, "if (modalIsOpen()) return;"
      assert_includes response.body, "fetch(validationUrl"
      refute_includes response.body, "input.value = resolvedCwd"
      assert_includes response.body, "function setNewSessionProjectMode(form)"
      assert_includes response.body, "function setNewSessionPathMode(form,"
      assert_includes response.body, "function syncNewSessionSelectedCwd(form)"
      assert_includes response.body, "data-new-session-new-path-option"
      assert_includes response.body, "hasAttribute(\"data-new-session-new-path-option\")"
      assert_includes response.body, "form.dataset.submitting === \"true\""
      assert_includes response.body, "function addSessionViewFormParams(formData)"
      assert_includes response.body, "form.action, { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } }"
      assert_includes response.body, "const sessionSearch = activeSidebarSessionSearch();"
      assert_includes response.body, "if (sessionSearch) formData.set(\"session_search\", sessionSearch);"
      refute_includes response.body, "showAllSessionsActive"
      refute_includes response.body, "activeSidebarSessionsLimit"
      assert_includes response.body, "const currentProject = new URLSearchParams(window.location.search).get(\"project\");"
      assert_includes response.body, "if (currentProject) url.searchParams.set(\"project\", currentProject);"
      assert_includes response.body, "let temporarySidebarSessionsLimit = null;"
      assert_includes response.body, "target.searchParams.set(\"sidebar_sessions_limit\", temporarySidebarSessionsLimit);"
      assert_includes response.body, "temporarySidebarSessionsLimit = targetUrl.searchParams.get(\"sidebar_sessions_limit\")"
      refute_includes response.body, "history.replaceState(history.state"
      assert_includes response.body, "function sidebarProjectFilterActive()"
      assert_includes response.body, "function sidebarSearchActive()"
      assert_includes response.body, "function sidebarControlActive()"
      assert_includes response.body, "if (sidebarControlActive() || recentlyInteractedWithSidebar())"
      assert_includes response.body, "if (sidebarControlActive()) {\n        scheduleSidebarRefresh(1000);\n        return;\n      }"
      assert_includes response.body, "async function changeSidebarProjectFilter(select)"
      assert_includes response.body, "replaceSidebarHtml(html, { scrollTop: 0, notify: false });"
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
      assert_nil selected["data-session-shortcut"]
      assert_nil selected.at_css(".session-shortcut")
      shortcuts = document.css(".recent-sessions a.session:not(.selected)").map { |link| [link["data-session-shortcut"], link.at_css(".session-shortcut")&.text] }
      assert_equal (1..8).map { |number| [number.to_s, number.to_s] }, shortcuts
    end
  end

  def test_sidebar_pins_current_session_before_sessions_without_header_or_shortcut
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 3)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/sidebar",
        params: { "session" => paths.first }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      links = document.css(".recent-sessions a.session")
      assert_equal ["Session 1", "Session 3", "Session 2"], links.map { |link| link.at_css(".session-title").text }
      assert links.first["class"].include?("selected")
      assert_nil links.first["data-session-shortcut"]
      assert_equal "Sessions", document.css(".recent-sessions-header h2").map(&:text).first
      assert_operator response.body.index("Session 1"), :<, response.body.index("<h2>Sessions</h2>")
    end
  end

  def test_sidebar_groups_unread_sessions_between_current_and_regular_sessions
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 4)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.first })
      File.write(paths[1], JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Unread done" }] } }) + "\n", mode: "a")
      File.write(paths.first, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Current done" }] } }) + "\n", mode: "a")

      response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => paths.first })

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      assert_equal ["Unread", "Sessions"], document.css(".recent-sessions-header h2").map(&:text)
      links = document.css(".recent-sessions a.session")
      assert_equal ["Session 1", "Session 2", "Session 4", "Session 3"], links.map { |link| link.at_css(".session-title").text }
      assert_nil links[0]["data-session-shortcut"]
      assert_equal ["1", "2", "3"], links[1..].map { |link| link["data-session-shortcut"] }
      assert links[1]["class"].include?("unread")
      refute links[0]["class"].include?("unread")
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
      assert_includes response.body, ".mobile-sessions-unread-badge"
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

  def test_sidebar_hides_unread_header_when_no_unread_sessions
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
      assert_equal 21, session_titles.length
      assert_equal "Session 41", session_titles.first
      assert_equal "Session 21", session_titles.last
      refute_includes response.body, "Session 20"
      load_more = document.at_css(".sidebar-load-more")
      assert load_more
      assert_equal "Load 20 more", load_more.text.gsub(/\s+/, " ").strip
      assert_includes load_more["href"], "sidebar_sessions_limit=40"
      assert load_more.at_css(".sidebar-load-more-spinner")
      assert_empty document.css(".cwd-group")
    end
  end

  def test_keeps_older_selected_session_visible_when_sidebar_is_trimmed
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 42)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.first }
      )

      assert_equal 200, response.status
      document = Nokogiri::HTML(response.body)
      session_titles = document.css(".recent-sessions a.session .session-title").map(&:text)
      assert_equal 21, session_titles.length
      assert_equal "Session 1", session_titles.first
      assert_equal "Session 42", session_titles[1]
      assert_equal "Session 23", session_titles.last
      assert_equal "Session 1", document.at_css(".recent-sessions a.session.selected .session-title").text
      assert_includes document.at_css(".sidebar-load-more")["href"], "sidebar_sessions_limit=40"
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
      assert_equal 41, session_titles.length
      assert_equal "Session 41", session_titles.first
      assert_equal "Session 1", session_titles.last
      refute document.at_css(".sidebar-load-more")
      session_link = document.at_css('.recent-sessions a.session[href*="session-1.jsonl"]')
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
      assert_equal "project", header.at_css(".session-header-project").text
      assert_equal "project", header.at_css(".session-header-project")["title"]
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
      assert_includes response.body, "messageRoleKey"
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
      refute_includes response.body, 'const turnButton = event.target.closest(".message-turn-button");'
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
              { type: "thinking", thinking: "**Heading**\n\nPrivate reasoning" },
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
      assert_includes response.body, 'Private reasoning'
      refute_includes response.body, '<div class="message-details-summary"><span class="compact-summary">thinking</span></div>'
      refute_includes response.body, "**Heading**"
      assert_includes response.body, "Private reasoning"
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
      assert_includes response.body, "function contentSegments(content, message = {})"
      assert_includes response.body, "appendCompactMessage(roleName, segment.summary, segment.text, segment.expanded"
      assert_includes response.body, "segment.rawDetails"
      refute_includes response.body, "Raw details"
      assert_includes response.body, "function renderToolSummary(container, parts, fallback)"
      assert_includes response.body, "message--tool-transcript"
      assert_includes response.body, "toolSummaryParts(toolName, toolPart?.arguments || {})"
      assert_includes response.body, "function transcriptToolCallText(name, args = {})"
      assert_includes response.body, 'if (["bash", "read"].includes(part.name)) return "";'
      assert_includes response.body, 'if (["edit", "write"].includes(part.name)) return transcriptToolCallText(part.name, part.arguments || {});'
      assert_includes response.body, 'return editPreview;'
      assert_includes response.body, '}).filter((segment) => segment.text || segment.compact);'
      assert_includes response.body, 'if (lines[lines.length - 1] === "") lines.pop();'
      assert_includes response.body, 'function renderToolTranscriptBody(body, text, toolName = "", options = {})'
      assert_includes response.body, 'body.dataset.rawText = text || "";'
      assert_includes response.body, 'body.classList.toggle("message-body--edit-preview", preview);'
      assert_includes response.body, 'const hasText = rawText !== "";'
      assert_includes response.body, 'collapse.hidden = !hasText;'
      assert_includes response.body, 'if (!hasText) {'
      assert_includes response.body, 'const shouldCollapse = collapse.dataset.toolOutputCollapsible === "true" && lines.length > TOOL_OUTPUT_DESKTOP_TAIL_LINES;'
      assert_includes response.body, 'fullTemplate?.content.replaceChildren(...toolOutputLineNodes(lines, toolName, preview, 0));'
      assert_includes response.body, 'tailTemplate?.content.replaceChildren(...toolOutputLineNodes(tailLines, toolName, preview, desktopExtraCount));'
      assert_includes response.body, 'control.hidden = true;'
      assert_includes response.body, 'span.className = `tool-diff-line ${toolDiffLineClass(line, preview)}`;'
      assert_includes response.body, 'renderToolTranscriptBody(entry.body, segment.text, segment.toolName || entry.toolName, { preview: segment.toolPreview === true });'
      assert_includes response.body, 'toolPreview: toolPart?.type === "toolCall" && toolName === "edit"'
      assert_includes response.body, 'bashCallEntry.body.classList.contains("message-body--edit-preview")'
      assert_includes response.body, 'segment.toolName === "bash" || (segment.toolTranscript && segment.error !== true && segment.toolName !== "write") ? segment.text'
      assert_includes response.body, '[bashCallEntry.body.dataset.rawText, segment.text].filter(Boolean).join("\\n\\n")'
      refute_includes response.body, 'details.open = options.open === true;'
      refute_includes response.body, 'collapseButton.textContent = "▴ Collapse details";'
      refute_includes response.body, 'event.target.closest("[data-collapse-details]")'
      assert_includes response.body, 'error: message.isError === true'
      refute_includes response.body, 'open: segment.expanded'
      assert_includes response.body, 'error: segment.error'
      assert_includes response.body, '["bash", "read", "edit", "write"].includes(segment.toolName)'
      assert_includes response.body, "part.type === \"toolCall\""
      assert_includes response.body, "part.type === \"thinking\""
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
      assert_includes response.body, "let liveToolExecutions = new Map();"
      assert_includes response.body, "function renderToolExecutionEvent(event)"
      assert_includes response.body, "event.type === \"tool_execution_update\""
      assert_includes response.body, "event.partialResult?.content"
      assert_includes response.body, "updateLiveToolExecution(entry, event, shouldScroll)"
      assert_includes response.body, "renderToolTranscriptBody(entry.body, toolExecutionText(event), event.toolName || entry.toolName)"
      assert_includes response.body, "appendCompactMessage(\"tool\", toolExecutionSummary(event), toolExecutionText(event)"
      assert_includes response.body, "{ toolName: event.toolName, error: event.isError === true }"
      assert_includes response.body, "if (!event.toolCallId || [\"bash\", \"read\", \"edit\", \"write\"].includes(event.toolName)) return;"
      assert_includes response.body, "if (segment.toolCallId && !segment.isToolResult && ![\"bash\", \"read\", \"edit\", \"write\"].includes(segment.toolName)) liveToolExecutions.set(segment.toolCallId, entry);"
      assert_includes response.body, 'if (["tool_execution_start", "tool_execution_update", "tool_execution_end"].includes(event.type))'
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
      assert_includes response.body, "function subagentDetailsFromEvent(event)"
      assert_includes response.body, "function subagentDisplayText(details, fallback, running = false)"
      assert_includes response.body, "function subagentResultRunning(details, result, index, running)"
      assert_includes response.body, 'if (result.stopReason === "stop") return false;'
      assert_includes response.body, 'if (event.toolName === "subagent")'
      refute_includes response.body, 'entry.details.open = subagentRunning(event);'
      refute_includes response.body, 'open: event.toolName === "subagent" && subagentRunning(event)'
      assert_includes response.body, 'if (event.toolName === "subagent") return subagentSummary(subagentDetailsFromEvent(event), subagentRunning(event));'
      assert_includes response.body, 'if (part.type === "toolCall") return items.push(`→ ${formatToolCallPlain(part.name, part.arguments || {})}`);'
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
      assert_includes response.body, "let autoScrollEnabled = true;"
      assert_includes response.body, "let forceBottomAutoScroll = false;"
      assert_includes response.body, "let programmaticScroll = false;"
      assert_includes response.body, "function nearConversationTop()"
      assert_includes response.body, "function latestReadableAssistantMessageIsVisible()"
      assert_includes response.body, "function applyAutoScroll(behavior = \"auto\")"
      assert_includes response.body, "function positionInitialConversationAtBottom()"
      assert_includes response.body, "function positionInitialConversationAtBottom() {\n      if (!conversationScroll) return;\n\n      autoScrollEnabled = true;"
      assert_includes response.body, "positionInitialConversationAtBottom();\n        loadOlderConversationHistory(sessionViewGeneration).catch(() => {});\n        requestAnimationFrame(() => {"
      assert_includes response.body, "requestAnimationFrame(() => requestAnimationFrame"
      assert_includes response.body, "function latestReadableAssistantMessage()"
      assert_includes response.body, "function latestMessageElement()"
      assert_includes response.body, "if (!forceBottomAutoScroll && latestAssistant && latestAssistant === latestMessageElement() && latestAssistant.offsetHeight > conversationScroll.clientHeight)"
      assert_includes response.body, "autoScrollEnabled = nearConversationBottom();"
      assert_includes response.body, "if (autoScrollEnabled && body.closest(\".message\") === latestReadableAssistantMessage()) scheduleAutoScroll();"
      assert_includes response.body, "if (shouldScroll && autoScrollEnabled) scheduleAutoScroll();"
      assert_includes response.body, "forceBottomAutoScroll = true;\n          applyAutoScroll(\"auto\");\n          forceBottomAutoScroll = false;"
      assert_includes response.body, "scrollToTop"
      refute_includes response.body, "const turnButton = event.target.closest(\".message-turn-button\");"
      refute_includes response.body, "turnButton.dataset.direction === \"previous\""
      refute_includes response.body, "scrollToUserMessage(target);"
      refute_includes response.body, "function topJumpControlsOffset()"
      refute_includes response.body, "return remSize * 3.5;"
      assert_includes response.body, "function scrollToBottom(behavior = \"auto\", { force = false } = {})"
      assert_includes response.body, "autoScrollEnabled = true;"
      assert_includes response.body, "forceBottomAutoScroll = force;"
      assert_includes response.body, "if (jumpToLatestButton.dataset.jumpTarget === \"message\") scrollToMessageBottom();"
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
      assert_includes response.body, "function messageTimestampKey(timestamp)"
      assert_includes response.body, "function messageFingerprint(roleName, text, timestampKey)"
      assert_includes response.body, "function liveMessageAlreadyRendered(roleName, text, timestampKey)"
      assert_includes response.body, "if (live && liveMessageAlreadyRendered(roleName, text, timestampKey)) return null;"
      assert_includes response.body, "function markLiveEntryRendered(entry, roleName, text, timestamp = null)"
      assert_includes response.body, "entry.article.remove();"
      assert_includes response.body, "function forgetLiveEntry(entry)"
      assert_includes response.body, "if (storedEntry === entry) liveAssistantSegments.delete(key);"
      assert_includes response.body, "if (storedEntry === entry) liveBashToolCalls.delete(key);"
      assert_includes response.body, "markLiveEntryRendered(bashCallEntry, bashCallEntry.article.dataset.role || \"assistant\", mergedText)"
      assert_includes response.body, "article.dataset.messageTimestamp = timestampKey;"
    end
  end

  def test_live_script_supports_ctrl_held_recent_session_shortcuts
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "function enterSessionShortcutMode()"
      assert_includes response.body, "event.key === \"Control\""
      assert_includes response.body, "if (!event.ctrlKey) return;"
      assert_includes response.body, "function recentSessionShortcutFromEvent(event)"
      assert_includes response.body, "event.code.match(/^Digit([1-9])$/)"
      assert_includes response.body, "event.code.match(/^Numpad([1-9])$/)"
      assert_includes response.body, "openRecentSessionShortcut(shortcut)"
      assert_includes response.body, "function currentSessionPath()"
      assert_includes response.body, "window.location.href = link.href;"
      refute_includes response.body, "clearUnreadSession(link.dataset.sessionPath)"
      assert_includes response.body, "exitSessionShortcutMode();\n      if (!link || !normalLeftClick(event)) return;"
      assert_includes response.body, "document.addEventListener(\"keyup\", (event) => {"
      assert_includes response.body, "window.addEventListener(\"blur\", exitSessionShortcutMode);"
      refute_includes response.body, "sessionShortcutTimer = setTimeout(exitSessionShortcutMode, 5000);"
      assert_includes response.body, "session-shortcuts-visible"
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
      assert_includes response.body, "function sidebarFragmentUrl(url = window.location.href)"
      assert_includes response.body, "function sidebarScrollContainer()"
      assert_includes response.body, "function bindSidebarScrollTracking()"
      assert_includes response.body, "function recentlyInteractedWithSidebar()"
      assert_includes response.body, "async function refreshSidebar(generation = sessionViewGeneration)"
      assert_includes response.body, "async function loadMoreSidebarSessions(button)"
      assert_includes response.body, "let sidebarUpdateGeneration = 0;"
      assert_includes response.body, "const sidebarGeneration = ++sidebarUpdateGeneration;"
      assert_includes response.body, "button.classList.add(\"is-loading\");"
      assert_includes response.body, "viewGeneration !== sessionViewGeneration || switchGeneration !== sessionSwitchGeneration || sidebarGeneration !== sidebarUpdateGeneration"
      assert_includes response.body, "replaceSidebarHtml(html, { scrollTop: previousScrollTop });"
      assert_includes response.body, "if (sidebarControlActive() || recentlyInteractedWithSidebar()) {\n        scheduleSidebarRefresh(1000);\n        return;\n      }"
      assert_includes response.body, "fetch(sidebarFragmentUrl())"
      assert_includes response.body, "const previousScrollTop = sidebarScrollContainer()?.scrollTop || 0;"
      assert_includes response.body, "const refreshedScrollContainer = sidebarScrollContainer();\n      if (refreshedScrollContainer) refreshedScrollContainer.scrollTop = scrollTop;"
      assert_includes response.body, "bindSidebarScrollTracking();"
      assert_includes response.body, "setTimeout(() => refreshSidebar().catch(() => {}), delay)"
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
      assert_includes response.body, "function bindSessionDom()"
      assert_includes response.body, "function switchSession(url, { push = true, focus = true } = {})"
      assert_includes response.body, "const switchGeneration = ++sessionSwitchGeneration;\n      let navigatingAway = false;\n      showSessionSwitching();\n      resetSessionViewState();"
      assert_includes response.body, "fetch(sessionFragmentUrl(url), { headers: { \"Accept\": \"application/json\" } })"
      assert_includes response.body, "if (switchGeneration !== sessionSwitchGeneration) return false;"
      assert_includes response.body, "if (link.classList.contains(\"selected\")) {\n        closeMobileSessionSidebar();\n        return;\n      }"
      assert_includes response.body, "function closeMobileSessionSidebar()"
      refute_includes response.body, "const previousSidebarScrollTop = sidebarScrollContainer()?.scrollTop || 0;"
      assert_includes response.body, "sessionSidebar.outerHTML = payload.sidebar_html;"
      assert_includes response.body, "conversationPanel.outerHTML = payload.conversation_html;"
      refute_includes response.body, "if (refreshedSidebarScrollContainer) refreshedSidebarScrollContainer.scrollTop = previousSidebarScrollTop;"
      assert_includes response.body, "history.pushState({ session: payload.session }"
      assert_includes response.body, "window.addEventListener(\"popstate\""
      assert_includes response.body, "closeMobileSessionSidebar();"
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
      assert_includes response.body, "const generation = sessionViewGeneration;"
      assert_includes response.body, "if (generation !== sessionViewGeneration || switchGeneration !== sessionSwitchGeneration || submittedSession !== promptSessionInput?.value) return;"
      assert_includes response.body, "const payload = await response.json().catch(() => null);\n        if (generation !== sessionViewGeneration || switchGeneration !== sessionSwitchGeneration || submittedSession !== promptSessionInput?.value) return;"
      assert_includes response.body, "function refreshSessionStatus(generation = sessionViewGeneration)"
      assert_includes response.body, "function renderModelStatus()"
      assert_includes response.body, "[liveStatusModel, liveStatusThinking ? `(${liveStatusThinking})` : null]"
      assert_includes response.body, "removeStatusItem(\"thinking\")"
      assert_includes response.body, "if (!response.ok || generation !== sessionViewGeneration || statusBar !== sessionStatusBar) return;"
      assert_includes response.body, "refreshSessionStatus(generation).catch(() => {});"
      assert_includes response.body, "function resetSessionViewState()"
      assert_includes response.body, "function markOptimisticUserMessageFailed(text)"
      assert_includes response.body, "const previousWaitingForOutputSince = waitingForOutputSince;"
      assert_includes response.body, "const submittedImageFiles = pendingImages.map((entry) => entry.file);"
      assert_includes response.body, "submittedImageFiles.forEach((file) => formData.append(\"images[]\", file, file.name || \"image\"));"
      assert_includes response.body, "const errorMessage = payload?.error || \"Prompt failed to send\";"
      assert_includes response.body, "if (promptTextarea && message) promptTextarea.value = message;"
      assert_includes response.body, "pendingImages = submittedImageFiles.map((file) => ({ file, url: URL.createObjectURL(file) }));"
      assert_includes response.body, "renderAttachments();"
      assert_includes response.body, "setComposerState(\"running\", \"Pi is running…\", previousWaitingForOutputSince);"
      assert_includes response.body, "markOptimisticUserMessageFailed(message);"
      assert_includes response.body, "message.hasAttribute(\"data-optimistic-text\") ? message.dataset.optimisticText : message.querySelector(\".message-body\")?.textContent"
      assert_includes response.body, "appendMessage(\"assistant\", `Prompt failed to send:\\n\\n${errorMessage}`, true, true, new Date(), { finalAssistantResponse: true });"
      assert_includes response.body, "clearTimeout(eventPollTimer);"
      assert_includes response.body, "eventPollInFlight = false;"
      assert_includes response.body, "sessionViewGeneration += 1;"
      assert_includes response.body, "if (!response.ok || generation !== sessionViewGeneration) return;"
      assert_includes response.body, "if (generation === sessionViewGeneration) {"
      assert_includes response.body, "scheduleNextEventPoll();"
      assert_includes response.body, "resetLiveAssistantTracking();"
      assert_includes response.body, "resetEventPollBackoff();"
      assert_includes response.body, "stopWaitingForOutput();"
      assert_includes response.body, "lastEventSeq = 0;"
      assert_includes response.body, "autoScrollEnabled = true;"
      assert_includes response.body, "clearAttachments();"
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
      assert_includes response.body, "event.target.closest('form[action=\"/sessions/new\"]')"
      assert_includes response.body, "headers: { \"Accept\": \"application/json\" }"
      assert_includes response.body, "const switchGeneration = sessionSwitchGeneration;"
      assert_includes response.body, "const viewGeneration = sessionViewGeneration;"
      assert_includes response.body, "showSessionSwitching();"
      assert_includes response.body, "if (switchGeneration !== sessionSwitchGeneration || viewGeneration !== sessionViewGeneration) return;"
      assert_includes response.body, "await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`"
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
      assert_includes response.body, "function resetEventCursor()"
      assert_includes response.body, "lastEventSeq = Number(liveOutput?.dataset.eventsAfter || 0);"
      assert_includes response.body, "resetEventCursor();"
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
      assert_includes response.body, "let eventPollInFlight = false;"
      assert_includes response.body, "let lastEventSeq = 0;"
      assert_includes response.body, "let waitingForOutputSince = null;"
      assert_includes response.body, "let emptyEventPollCount = 0;"
      assert_includes response.body, "function scheduleNextEventPoll(delay = eventPollDelay())"
      assert_includes response.body, "if (eventPollInFlight) return;"
      assert_includes response.body, "eventPollInFlight = true;"
      assert_includes response.body, "const eventsUrl = new URL(liveOutput.dataset.eventsUrl, window.location.origin);"
      assert_includes response.body, "eventsUrl.searchParams.set(\"after\", lastEventSeq);"
      assert_includes response.body, "lastEventSeq = payload.last_seq;"
      assert_includes response.body, "if (payload.missed) {"
      assert_includes response.body, "await refreshCurrentSessionPreservingComposer();"
      assert_includes response.body, "eventPollInFlight = false;"
      assert_includes response.body, "if (document.hidden) return 10000;"
      assert_includes response.body, "if (emptyEventPollCount >= 6) return 5000;"
      assert_includes response.body, "if (emptyEventPollCount >= 2) return 2000;"
      assert_includes response.body, "emptyEventPollCount = payload.events.length > 0 ? 0 : emptyEventPollCount + 1;"
      assert_includes response.body, "resetEventPollBackoff();"
      assert_includes response.body, "startWaitingForOutput();"
      assert_includes response.body, "stopWaitingForOutput();"
      assert_includes response.body, "scheduleNextEventPoll(0);"
      assert_includes response.body, "let eventPollAbortController = null;"
      assert_includes response.body, "const pollTimeout = setTimeout(() => controller.abort(), 12000);"
      assert_includes response.body, "updateWaitingForOutputStatus();"
      assert_includes response.body, "signal: controller.signal"
      assert_includes response.body, "const staleSessionRefreshAfterMs = 60 * 1000;"
      assert_includes response.body, "let staleSessionRefreshInFlight = false;"
      assert_includes response.body, "async function refreshStaleSessionAfterResume(hiddenDuration = 0)"
      assert_includes response.body, "if (staleSessionRefreshInFlight) return true;"
      assert_includes response.body, "const pollingGap = Date.now() - lastEventPollSuccessAt;"
      assert_includes response.body, "if (hiddenDuration < staleSessionRefreshAfterMs && pollingGap < staleSessionRefreshAfterMs) return false;"
      assert_includes response.body, "staleSessionRefreshInFlight = true;"
      assert_includes response.body, "return await refreshCurrentSessionPreservingComposer();"
      assert_includes response.body, "staleSessionRefreshInFlight = false;"
      assert_includes response.body, "async function resumeEventPolling(hiddenDuration = 0)"
      assert_includes response.body, "abortEventPoll();"
      assert_includes response.body, "if (await refreshStaleSessionAfterResume(hiddenDuration)) return;"
      assert_includes response.body, "window.addEventListener(\"pageshow\", () => resumeEventPolling().catch(() => {}));"
      assert_includes response.body, "window.addEventListener(\"focus\", () => resumeEventPolling().catch(() => {}));"
      assert_includes response.body, "window.addEventListener(\"online\", () => resumeEventPolling().catch(() => {}));"
      refute_includes response.body, "setInterval(() => pollEvents()"
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
      assert_includes response.body, "function showReconnectBanner()"
      assert_includes response.body, "function hideReconnectBanner()"
      assert_includes response.body, "function composerDraft()"
      assert_includes response.body, "message: promptTextarea?.value || \"\""
      assert_includes response.body, "images: pendingImages.map((entry) => entry.file)"
      assert_includes response.body, "function restoreComposerDraft(draft)"
      assert_includes response.body, "if (!draft || promptSessionInput?.value !== draft.session) return;"
      assert_includes response.body, "if (draft.images.length > 0) addImageFiles(draft.images);"
      assert_includes response.body, "function refreshCurrentSessionPreservingComposer()"
      assert_includes response.body, "const refreshed = await switchSession(window.location.href, { push: false, focus: false });"
      assert_includes response.body, "if (refreshed) restoreComposerDraft(draft);"
      assert_includes response.body, "function reconnectSession()"
      assert_includes response.body, "await refreshCurrentSessionPreservingComposer();"
      assert_includes response.body, "reconnectButton?.addEventListener(\"click\", reconnectSession);"
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
      assert_includes response.body, "let liveAssistantMessage = null;"
      assert_includes response.body, "let liveAssistantSegments = new Map();"
      assert_includes response.body, "let liveAssistantSeen = false;"
      assert_includes response.body, "let liveUserMessages = new Map();"
      assert_includes response.body, "let restorePromptFocusAfterSending = false;"
      assert_includes response.body, "let escapeStopConfirmationExpiresAt = 0;"
      assert_includes response.body, "const ESCAPE_STOP_CONFIRMATION_WINDOW_MS = 2000;"
      assert_includes response.body, "function optimisticUserMessage(text)"
      assert_includes response.body, "function upsertLiveUserSegment(event, segment, fallbackIndex, shouldScroll, timestamp)"
      assert_includes response.body, 'if (live && roleName === "user" && !options.optimistic && optimisticUserMessageAlreadyRendered(text)) return null;'
      assert_includes response.body, 'if (options.optimistic) {'
      assert_includes response.body, "article.dataset.optimisticText = options.optimisticText ?? text;"
      assert_includes response.body, 'upsertLiveUserSegment(event, segment, index, shouldScroll, timestamp);'
      assert_includes response.body, 'const displayText = roleName === "user" && entry.userDisplayText ? entry.userDisplayText : segment.text;'
      assert_includes response.body, 'const entry = { article, body, compact: false, userDisplayText: body?.textContent || segment.text };'
      assert_includes response.body, "function formatTimestamp(timestamp)"
      assert_includes response.body, "date.getHours()"
      refute_includes response.body, "date.getUTCHours()"
      assert_includes response.body, "function eventTimestamp(event)"
      assert_includes response.body, "function eventErrorText(event)"
      assert_includes response.body, "renderErrorEvent(event)"
      assert_includes response.body, "let liveErrorSeen = false;"
      assert_includes response.body, "liveErrorSeen = true;"
      assert_includes response.body, "appendMessage(\"error\", errorText, true, true, eventTimestamp(event));"
      assert_includes response.body, 'appendMessage("assistant", segment.text, true, shouldScroll, timestamp, { thinking: segment.thinking, finalAssistantResponse });'
      assert_includes response.body, 'function renderAssistantMarkdown(body, text, delay = 120)'
      assert_includes response.body, 'body.dataset.rendering = "pending";'
      assert_includes response.body, 'clearTimeout(body.markdownRenderTimeout);'
      assert_includes response.body, 'fetch("/markdown", { method: "POST", body: formData })'
      assert_includes response.body, 'if (["custom", "system", "status"].includes(role)) return "status";'
      assert_includes response.body, "function showStatus(_text, _forceScroll = false) {}"
      assert_includes response.body, "showStatus(eventStatusText(event));"
      assert_includes response.body, "function renderCompactionEvent(event)"
      assert_includes response.body, "appendCompactMessage(\"status\", \"Conversation compacted\", event.summary || \"Compaction completed\""
      assert_includes response.body, "refreshSessionStatus().catch(() => {});"
      assert_includes response.body, "if (event.type === \"compaction\") {\n        renderCompactionEvent(event);\n        return;\n      }"
      assert_includes response.body, "if (event.type === \"compaction_start\") resetLiveCompactionTracking();"
      assert_includes response.body, "if (event.type === \"compaction_end\") {\n          removePendingCompactionMessage();\n          if (!event.aborted && !liveCompactionRendered) renderCompactionEvent(event);\n          if (!liveAgentRunning) setComposerState(\"done\", event.aborted ? \"Compaction aborted\" : \"Done\");\n          if (!event.aborted) refreshSessionStatus().catch(() => {});\n          refreshSidebar().catch(() => {});\n        }"
      assert_includes response.body, "if (/^\\/(?:name|rename)$/.test(trimmed)) return { valid: false };"
      assert_includes response.body, "if (/^\\/(?:name|rename)[ \\t]+[^\\r\\n]+$/.test(trimmed)) return { valid: true };"
      assert_includes response.body, "function sessionNameSlashCommand(message)"
      assert_includes response.body, "function sessionForkSlashCommand(message)"
      assert_includes response.body, "function sessionTreeSlashCommand(message)"
      assert_includes response.body, "function sessionCloneSlashCommand(message)"
      assert_includes response.body, "function sessionNewSlashCommand(message)"
      assert_includes response.body, "function updateSessionHeaderName(name)"
      assert_includes response.body, "function sessionTitleFromEvent(event)"
      assert_includes response.body, "if (event.type === \"session_info\") return event.name;"
      assert_includes response.body, "if (event.type === \"custom\" && event.customType === \"pi-extensions-session-title\") return event.data?.title;"
      assert_includes response.body, "if (event.type === \"custom_message\" && event.customType === \"session-title-update\")"
      assert_includes response.body, "updateSessionHeaderName(sessionTitleFromEvent(event));"
      assert_includes response.body, "function updateHeaderFromSelectedSidebarSession()"
      assert_includes response.body, "const selectedTitle = sessionSidebar?.querySelector(\"a.session.selected .session-title\")?.textContent.trim();"
      assert_includes response.body, "updateHeaderFromSelectedSidebarSession();"
      assert_includes response.body, "const renameCommand = followUp ? null : sessionNameSlashCommand(message);"
      assert_includes response.body, "const compactCommand = followUp ? null : sessionCompactSlashCommand(message);"
      assert_includes response.body, "const forkCommand = followUp ? null : sessionForkSlashCommand(message);"
      assert_includes response.body, "const treeCommand = followUp ? null : sessionTreeSlashCommand(message);"
      assert_includes response.body, "const cloneCommand = followUp ? null : sessionCloneSlashCommand(message);"
      assert_includes response.body, "const newCommand = followUp ? null : sessionNewSlashCommand(message);"
      assert_includes response.body, "if (!renameCommand && !compactCommand && !forkCommand && !treeCommand && !cloneCommand && !newCommand) {"
      assert_includes response.body, "if (!followUp) {\n          resetLiveAssistantTracking();\n          document.querySelectorAll(\".tree-position-banner\").forEach((banner) => banner.remove());\n          appendMessage(\"user\", [message, submittedImageFiles.length > 0"
      assert_includes response.body, "true, true, new Date(), { optimistic: true, optimisticText: message });\n        }"
      assert_includes response.body, "resetEventPollBackoff();"
      assert_includes response.body, "scheduleNextEventPoll(0);"
      assert_includes response.body, "if (payload?.command === \"rename\") {\n          if (payload.error) {\n            setComposerState(\"error\", payload.error);\n            showStatus(payload.error, true);\n            return;\n          }\n          if (payload?.session && promptSessionInput && payload.session !== promptSessionInput.value) {\n            await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });\n            return;\n          }\n          updateSessionHeaderName(payload.name);\n          setComposerState(\"done\", \"Renamed\");\n          showStatus(eventStatusText({ type: \"session_info\", name: payload.name }), true);\n          refreshSidebar().catch(() => {});\n          return;\n        }"
      assert_includes response.body, "appendPendingCompactionMessage(new Date());"
      assert_includes response.body, "markSidebarSessionCompacting(submittedSession);"
      assert_includes response.body, "if (payload?.command === \"compact\") {\n          refreshSidebar().catch(() => {});\n          setComposerState(\"running\", \"Compacting…\");\n          showStatus(\"Compaction started\", true);\n          return;\n        }"
      assert_includes response.body, "if (payload?.command === \"fork\") {\n          setComposerState(\"idle\");\n          showStatus(\"Choose a fork point\", true);\n          openForkSessionModal();\n          return;\n        }"
      assert_includes response.body, "if (payload?.command === \"tree\") {\n          setComposerState(\"idle\");\n          showStatus(\"Choose a tree entry\", true);\n          openTreeSessionModal();\n          return;\n        }"
      assert_includes response.body, "promptForm.requestSubmit();"
      assert_includes response.body, "function resizePromptTextarea()"
      assert_includes response.body, "commandList?.removeAttribute(\"open\");"
      assert_includes response.body, "function filterCommandsFromPrompt()"
      assert_includes response.body, "Slash commands are not supported in queued follow-up messages."
      assert_includes response.body, "function composingFollowUp()"
      assert_includes response.body, "if (composingFollowUp()) return showQueuedSlashCommandMessage();"
      assert_includes response.body, "const query = promptTextarea.value.startsWith(\"/\") ? promptTextarea.value.slice(1).trim().toLowerCase() : \"\";"
      assert_includes response.body, "function selectHighlightedCommand()"
      assert_includes response.body, "setComposerState(\"running\", \"Pi is running…\");"
      assert_includes response.body, "composerState.textContent = \"Press ESC again to stop current task\";"
      assert_includes response.body, "composerStopButton = document.querySelector(\".session-header .composer-stop-button\") || null;"
      assert_includes response.body, "const agentBusy = [\"running\", \"sending\"].includes(state);"
      assert_includes response.body, "promptTextarea.disabled = submitting;"
      assert_includes response.body, "if (!submitting && restorePromptFocusAfterSending)"
      assert_includes response.body, "promptTextarea.focus({ preventScroll: true });"
      assert_includes response.body, "composerStopButton.hidden = !agentBusy;"
      assert_includes response.body, "if (followUp) formData.set(\"streaming_behavior\", \"follow_up\");"
      assert_includes response.body, "const attachmentsDisabled = submitting;"
      assert_includes response.body, "addImageFiles(files);"
      assert_includes response.body, "function confirmOrStopRunningTask(event)"
      assert_includes response.body, "if (composerState?.dataset.state !== \"running\") return false;"
      assert_includes response.body, "if (event.repeat) return true;"
      assert_includes response.body, "showStatus(\"Press ESC again to stop current task\", true);"
      assert_includes response.body, "if (composerState) composerState.textContent = \"Stopping current task…\";"
      assert_includes response.body, "abortForm.requestSubmit();"
      assert_includes response.body, "if (event.key === \"Escape\" && confirmOrStopRunningTask(event)) return;"
      assert_includes response.body, "Send follow-up…"
    end
  end

  def test_live_event_script_notifies_when_final_assistant_reply_arrives_outside_active_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "function notifyFinalAssistantReply(event)"
      assert_includes response.body, "if (roleName !== \"assistant\" || event.type !== \"message_end\") return;"
      assert_includes response.body, "if (sessionIsActivelyViewed(sessionPath)) return;"
      assert_includes response.body, "const body = notificationReplyPreview(finalAssistantReplyText(message));"
      assert_includes response.body, "showPiNotification(name, body, window.location.href, `pi-final-reply:${sessionPath}`)"
      assert_includes response.body, "notifyFinalAssistantReply(event);"
    end
  end

  def test_sidebar_refresh_notifies_for_background_final_replies
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "const previousAssistantCounts = sidebarAssistantResponseCounts();"
      assert_includes response.body, "notifyBackgroundFinalReplies(previousAssistantCounts);"
      assert_includes response.body, "if (sessionPath === currentSessionPath()) return;"
      assert_includes response.body, "const key = [sessionPath, currentCount].join(\":\");"
      assert_includes response.body, "const body = notificationReplyPreview(link.dataset.latestAssistantResponsePreview);"
      assert_includes response.body, "showPiNotification(name, body, sessionUrl(sessionPath), `pi-final-reply:${sessionPath}`)"
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
      assert_includes response.body, "function segmentIdentity(event, segment, fallbackIndex)"
      assert_includes response.body, "event.assistantMessageEvent || {}"
      assert_includes response.body, "segment.startIndex ?? update.contentIndex ?? fallbackIndex"
      assert_includes response.body, "function upsertLiveAssistantSegment(event, roleName, segment, fallbackIndex, shouldScroll, timestamp)"
      assert_includes response.body, "const existing = liveAssistantSegments.get(key);"
      assert_includes response.body, "const updated = updateLiveSegment(existing, roleName, segment, shouldScroll, timestamp);"
      assert_includes response.body, "liveAssistantSegments.set(key, entry);"
      assert_includes response.body, "if (roleName === \"assistant\" && event.type === \"message_start\") resetLiveAssistantTracking();"
      assert_includes response.body, "let liveAgentRunning = false;"
      assert_includes response.body, "if (event.type === \"turn_end\") {"
      assert_includes response.body, "if (!liveErrorSeen) {\n          if (liveAssistantSeen) showStatus(\"Done\");\n          if (!liveAgentRunning) setComposerState(\"done\", \"Done\");\n        }"
      assert_includes response.body, "if (event.type === \"agent_end\") {"
      assert_includes response.body, "liveAgentRunning = false;\n        if (renderErrorEvent(event)) {\n          resetLiveAssistantTracking();\n          return;\n        }\n        if (!liveErrorSeen) {\n          if (liveAssistantSeen) showStatus(\"Done\");\n          setComposerState(\"done\", \"Done\");\n        }"
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

      page_response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => first_path })
      assert_includes page_response.body, "a.session.unread .session-title"
      assert_includes page_response.body, "a.session.unread .session-indicators::before"
      assert_includes page_response.body, "content: \"new\""
      refute_includes page_response.body, "localStorage.getItem(\"piSidebarUnreadSessions\")"

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
      assert_includes initial_response.body, "function sidebarFragmentUrl(url = window.location.href)"
      assert_includes initial_response.body, "if (!sidebarUrl.searchParams.has(\"session\"))"
      assert_includes initial_response.body, "a.session.selected[data-session-path]"

      File.write(first_path, JSON.generate({ type: "message", message: { role: "assistant", content: [{ type: "text", text: "Background done" }] } }) + "\n", mode: "a")
      Rack::MockRequest.new(PiWebGateway).get("/sidebar")

      unread_response = Rack::MockRequest.new(PiWebGateway).get("/sidebar", params: { "session" => second_path })
      assert_includes unread_response.body, "class=\"session recent-session unread"
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
      assert_includes response.body, "const initialComposerState = liveOutput.dataset.composerState;"
      assert_includes response.body, "const initialComposerStateSince = Number(liveOutput.dataset.composerStateSince || 0);"
      assert_includes response.body, "liveAgentRunning = liveOutput.dataset.agentRunning === \"true\";"
      assert_includes response.body, "setComposerState(initialComposerState, \"Pi is running…\", initialComposerStateSince);"
      assert_includes response.body, "if (state === \"running\" && (since || !waitingForOutputSince)) startWaitingForOutput(since || Date.now());"
      assert_includes response.body, "payload.events.length > 0 && composerState?.dataset.state === \"running\" && !waitingForOutputSince"
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
      status = scroll.at_css("[data-conversation-history-status]")
      assert_equal "Loading earlier messages…", status.text.strip
      assert_includes response.body, ".conversation-history-status"
      assert_includes response.body, "function finishConversationHistoryStatus()"
      assert_includes response.body, "function failConversationHistoryStatus()"
      assert_includes response.body, "loadOlderConversationHistory"
      assert_includes response.body, "previousHeight"
      assert_includes response.body, "conversationScroll.scrollTop = previousTop + (conversationScroll.scrollHeight - previousHeight)"
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

  def test_raw_details_endpoint_rejects_missing_and_outside_root_sessions
    Dir.mktmpdir do |dir|
      Dir.mktmpdir do |outside_dir|
        valid_path = write_session_with_raw_messages(dir, [
          { type: "compaction", timestamp: "2026-06-13T10:00:00Z", summary: "Compacted", raw: "raw" }
        ])
        outside_path = write_session_with_messages(outside_dir, [{ role: "assistant", text: "Outside" }])
        PiWebGateway.set :sessions_root, dir
        PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

        missing_response = Rack::MockRequest.new(PiWebGateway).get(
          "/message_raw_details",
          params: { "session" => File.join(dir, "missing.jsonl"), "message_index" => "0" }
        )
        outside_response = Rack::MockRequest.new(PiWebGateway).get(
          "/message_raw_details",
          params: { "session" => outside_path, "message_index" => "0" }
        )
        invalid_index_response = Rack::MockRequest.new(PiWebGateway).get(
          "/message_raw_details",
          params: { "session" => valid_path, "message_index" => "not-a-number" }
        )
        missing_token_response = Rack::MockRequest.new(PiWebGateway).get(
          "/message_raw_details",
          params: { "session" => valid_path, "message_index" => "0" }
        )

        assert_equal 404, missing_response.status
        assert_equal 404, outside_response.status
        assert_equal 404, invalid_index_response.status
        assert_equal 404, missing_token_response.status
      end
    end
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
      assert_includes response.body, "color-scheme: dark"
      assert_includes response.body, "session-running-indicator"
      refute_includes response.body, ">active</span>"
      assert_includes response.body, "copy-button"
      assert_includes response.body, "code-block-copy-button"
      assert_includes response.body, "data-copy-target"
      assert_includes response.body, "enhanceMarkdownCodeBlocks(body)"
      assert_includes response.body, 'button.dataset.copyTarget === "code-block"'
      assert_includes response.body, "navigator.clipboard.writeText"
      assert_includes response.body, "window.isSecureContext"
      assert_includes response.body, "document.execCommand(\"copy\")"
      assert_includes response.body, "Copy failed"
      assert_includes response.body, "empty-state"
      assert_includes response.body, "button:hover"
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

      assert_equal 404, status_response.status
      assert_equal 404, prompt_response.status
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

  def compact_card_with_summary(document, summary)
    document.css(".message--compact").find do |card|
      card.at_css(".compact-summary")&.text == summary
    end
  end

  class FakeRpcClient
    def initialize(calls, events_or_commands = [], session_file = nil)
      @calls = calls
      @events = events_or_commands
      @commands = events_or_commands
      @session_file = session_file
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
      { "type" => "response", "command" => "get_state", "success" => true, "data" => { "sessionFile" => @session_file } }
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
    File.write(path, JSON.generate({ type: "session", id: "session-1", cwd: project_cwd(root) }) + "\n")
    path
  end

  def write_sessions(root, count:)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    FileUtils.mkdir_p(project_cwd(root))

    (1..count).map do |index|
      path = File.join(session_dir, "session-#{index}.jsonl")
      File.write(path, [
        JSON.generate({ type: "session", id: "session-#{index}", cwd: project_cwd(root) }),
        JSON.generate({ type: "session_info", name: "Session #{index}" })
      ].join("\n") + "\n")
      FileUtils.touch(path, mtime: Time.at(index))
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

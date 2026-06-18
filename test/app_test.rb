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
    PiWebGateway.set :attachments_root, @attachments_root
    PiWebGateway.set :read_state_path, File.join(@read_state_root, "read-state.json")
    PiWebGateway.set :browser_access_path, File.join(@browser_access_root, "browser-access.json")
    PiWebGateway.set :gateway_admin_password, nil
    PiWebGateway.set :rpc_client_registry, nil
    PiWebGateway.set :pending_rpc_cwds, {}
    PiWebGateway.set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
    PiWebGateway.set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd) }]
  end

  def teardown
    FileUtils.remove_entry(@attachments_root) if @attachments_root && Dir.exist?(@attachments_root)
    FileUtils.remove_entry(@read_state_root) if @read_state_root && Dir.exist?(@read_state_root)
    FileUtils.remove_entry(@browser_access_root) if @browser_access_root && Dir.exist?(@browser_access_root)
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

  def test_json_prompt_redirect_preserves_sidebar_view_state
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
      assert_includes payload.fetch("redirect"), "expanded_cwd"
      assert_includes payload.fetch("redirect"), "show_all_sessions=1"
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

  def test_posts_steering_prompt_to_selected_session
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
        params: { "session" => path, "message" => "Actually do this", "streaming_behavior" => "steer" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :steer, "Actually do this" ]], calls
      assert_equal true, JSON.parse(response.body).fetch("steer")
    end
  end

  def test_steering_prompt_treats_rename_slash_command_as_message
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
        params: { "session" => path, "message" => "/name keep steering", "streaming_behavior" => "steer" },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal [[ :start, path ], [ :steer, "/name keep steering" ]], calls
      payload = JSON.parse(response.body)
      assert_equal true, payload.fetch("steer")
      refute payload.key?("command")
    end
  end

  def test_rejects_steering_prompt_with_uploaded_images
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
        params: { "session" => path, "message" => "Actually do this", "streaming_behavior" => "steer", "images[]" => upload },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 422, response.status
      assert_empty calls
      assert_equal "Steering messages cannot include images", JSON.parse(response.body).fetch("error")
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
      assert_equal [
        [:start, path],
        [:prompt, "What is this?", [{ type: "image", data: Base64.strict_encode64("fake image data"), mimeType: "image/png" }]]
      ], calls
    end
  end

  def test_renders_historical_attachment_badge_for_uploaded_image_prompt
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
          message: { role: "user", content: [{ type: "text", text: "What is this?" }] }
        ))
      end

      response = Rack::MockRequest.new(PiWebGateway).get("/", params: { "session" => path })

      assert_equal 303, post_response.status
      assert_equal 200, response.status
      assert_includes response.body, "message-attachments"
      assert_includes response.body, "📎 1 image attachment"
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }
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
      assert_includes payload.fetch("redirect"), "expanded_cwd"
      assert_includes payload.fetch("redirect"), "show_all_sessions=1"
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
      assert_includes payload.fetch("redirect"), "show_all_sessions=1"
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
      assert_equal project_cwd(dir), PiWebGateway.pending_rpc_cwds.fetch(forked_path)

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
      assert_includes payload.fetch("redirect"), "show_all_sessions=1"
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => real_path }
      )

      assert_equal 200, response.status
      assert_equal({ "events" => [], "last_seq" => 0, "missed" => false }, JSON.parse(response.body))
      refute registry.active?(real_path)
      assert registry.active?(pending_path)
      assert_includes PiWebGateway.pending_rpc_cwds, pending_path
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => real_path, "message" => "Continue" }
      )

      assert_equal 303, response.status
      assert registry.active?(real_path)
      refute registry.active?(pending_path)
      refute_includes PiWebGateway.pending_rpc_cwds, pending_path
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

      response = Rack::MockRequest.new(PiWebGateway).post(
        "/prompt",
        params: { "session" => pending_path, "message" => "Continue" }
      )

      assert_equal 303, response.status
      assert_includes response["Location"], Rack::Utils.escape(real_path)
      refute_includes response["Location"], Rack::Utils.escape(pending_path)
      assert registry.active?(real_path)
      refute registry.active?(pending_path)
      refute_includes PiWebGateway.pending_rpc_cwds, pending_path
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

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
        FakeRpcClient.new(calls, [{ "name" => "review", "source" => "skill", "description" => "Review code" }])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get("/commands", params: { "session" => path })

      assert_equal 200, response.status
      assert_includes response.body, "Slash commands (1)"
      assert_includes response.body, "/review"
      assert_includes response.body, "Review code"
      refute_includes response.body, "<code>/new</code>"
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
      assert_includes response.body, "Slash commands (0)"
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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

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
      PiWebGateway.set :pending_rpc_cwds, { pending_path => project_cwd(dir) }

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
      assert_includes response.body, "Send to queue…"
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
      assert_includes response.body, ".message--tool .message-details summary, .message--tool-transcript .message-details summary { max-width: 100%; overflow-x: auto; white-space: nowrap; }"
      assert_includes response.body, ".message--tool .message-body, .message--tool-transcript .message-body, .raw-details pre { max-width: 100%; overflow-x: auto; }"
      assert_includes response.body, "scrollbar-width: none"
      assert_includes response.body, ".message--user { margin-left: 10%; background: #343541; border-color: rgba(69, 133, 255, 0.72); color: #d4d4d4; }"
      assert_includes response.body, ".message--assistant { margin-right: 10%; background: #080d20; border-color: rgba(69, 133, 255, 0.32); color: #f0c7a4; }"
      assert_includes response.body, ".message--thinking { margin-right: 16%; background: #080d20; border-color: rgba(69, 133, 255, 0.22); border-style: dashed; color: #7f7f88; box-shadow: none; }"
      assert_includes response.body, ".message--tool { background: #080d20; border-color: rgba(69, 133, 255, 0.32); border-style: dashed; color: var(--text); }"
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
      assert_equal "New session +", button.text.strip
      modal = document.at_css('body > [data-modal="new-session-modal"]')
      assert modal
      assert_equal "/sessions/new_at_cwd", modal.at_css('form.new-session-cwd-form')["action"]
      assert_equal "1", modal.at_css('input[name="show_all_sessions"]')["value"]
      assert_includes modal.css('option').map { |option| option["value"] }, project_cwd(dir)
      assert_includes modal.text, "Start session"
      assert_includes modal.text, "Existing folder"
      assert_includes modal.text, "Path"
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
      assert_equal [older_cwd, newer_cwd], options.map { |option| option["value"] }.reject(&:empty?)
      selected_option = options.find { |option| option["selected"] }
      assert_equal older_cwd, selected_option["value"]
      assert_equal older_cwd, modal.at_css('input[data-new-session-cwd-input]')["value"]
      refute modal.at_css('button[data-new-session-submit]').key?("disabled")
      assert_includes modal.at_css('[data-new-session-cwd-message]').text, "Directory exists."
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
      assert_equal [filtered_cwd, newer_cwd, current_cwd], options.map { |option| option["value"] }.reject(&:empty?)
      selected_option = options.find { |option| option["selected"] }
      assert_equal filtered_cwd, selected_option["value"]
      assert_equal filtered_cwd, modal.at_css('input[data-new-session-cwd-input]')["value"]
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
      assert_equal filtered_cwd, modal.at_css('input[data-new-session-cwd-input]')["value"]
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
      assert_equal filtered_cwd, modal.at_css('input[data-new-session-cwd-input]')["value"]
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
      assert_includes response.body, "data-modal-open=\"fork-session-modal\""
      assert_includes response.body, "class=\"clone-session-form\""
      assert_includes response.body, "function loadForkMessages(modal)"
      assert_includes response.body, "fetch(\"/sessions/fork\", { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } })"
      assert_includes response.body, "await switchToBranchedSession(payload);"
      assert_includes response.body, "const originalForkText = forkOption.textContent;"
      assert_includes response.body, "forkOption.textContent = originalForkText;"
      assert_includes response.body, "showStatus(\"Could not fork this session\", true);"
      assert_includes response.body, "function makeForkButton(entryId)"
      assert_includes response.body, "function forkEntryIdFromEvent(event, message)"
      assert_includes response.body, "return message?.entryId || message?.entry_id || event.entryId || event.entry_id || message?.id"
      assert_includes response.body, "ensureForkButton(updated, entryId);"
      assert_includes response.body, "appendMessage(\"user\", segment.text, true, shouldScroll, timestamp, { entryId });"
      assert_includes response.body, "abortEventPoll();"
      assert_includes response.body, "async function submitAbort(event)"
      assert_includes response.body, "if (modalIsOpen()) return;"
      assert_includes response.body, "fetch(validationUrl"
      assert_includes response.body, "if (select && select.value !== input.value.trim()) select.value = \"\";"
      assert_includes response.body, "form.dataset.submitting === \"true\""
      assert_includes response.body, "if (showAllSessionsActive()) {\n        formData.set(\"show_all_sessions\", \"1\");"
      assert_includes response.body, "form.action, { method: \"POST\", body: formData, headers: { \"Accept\": \"application/json\" } }"
      assert_includes response.body, "formData.set(\"sidebar_sessions_limit\", sidebarSessionsLimit);"
      assert_includes response.body, "const currentProject = new URLSearchParams(window.location.search).get(\"project\");"
      assert_includes response.body, "if (currentProject) url.searchParams.set(\"project\", currentProject);"
      assert_includes response.body, "function sidebarProjectFilterActive()"
      assert_includes response.body, "if (sidebarProjectFilterActive() || recentlyInteractedWithSidebar())"
      assert_includes response.body, "if (sidebarProjectFilterActive()) {\n        scheduleSidebarRefresh(1000);\n        return;\n      }"
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
      assert_includes payload.fetch("url"), "expanded_cwd"
      assert_includes payload.fetch("sidebar_html"), "session-sidebar"
      refute_includes payload.fetch("sidebar_html"), "expanded_cwd"
      assert_includes payload.fetch("sidebar_html"), "selected"
      assert_includes payload.fetch("conversation_html"), "conversation-panel"
      assert_includes payload.fetch("conversation_html"), "expanded_cwd"
      assert_includes payload.fetch("conversation_html"), paths.first
      assert_includes payload.fetch("conversation_html"), "project"
      assert_includes payload.fetch("conversation_html"), "session-header-project"
    end
  end

  def test_renders_fork_button_for_user_messages_with_entry_ids
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
      fork_button = user_message.at_css('[data-fork-entry-id="user-entry-1"]')
      assert_equal "Fork", fork_button.text.strip
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
      assert_includes response.body, '<span class="thinking-prefix">Thinking:</span> Private reasoning'
      refute_includes response.body, '<summary><span class="compact-summary">thinking</span></summary>'
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
      assert_includes response.body, "<pre><code class=\"ruby\">puts :ok\n</code></pre>"
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
      assert_includes response.body, '<span class="thinking-prefix">Thinking:</span> Thinking through the problem'
      refute_includes response.body, '<summary><span class="compact-summary">thinking</span></summary>'
      assert_includes response.body, '<summary><span class="compact-summary">$ ls</span></summary>'
      assert_includes response.body, 'class="message message--tool message--compact" data-role="toolResult"'
      assert_includes response.body, '<summary><span class="compact-summary">bash</span></summary>'
      assert_includes response.body, 'class="message message--tool message--compact message--tool-error" data-role="toolResult"'
      refute_includes response.body, '<details class="message-details" open>'
      assert_includes response.body, "Thinking through the problem"
      assert_includes response.body, "file list"
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
            content: [{ type: "toolCall", id: "write-1", name: "write", arguments: { path: "notes/status.txt", content: "done" } }]
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
      assert_includes response.body, 'message--tool-transcript'
      assert_includes response.body, '<summary><span class="compact-summary"><span class="tool-command">read</span> <span class="tool-path">test/app_test.rb</span><span class="tool-range">:545-654</span></span></summary>'
      assert_includes response.body, '<summary><span class="compact-summary"><span class="tool-command">edit</span> <span class="tool-path">test/pi_session_store_test.rb</span></span></summary>'
      assert_includes response.body, '<summary><span class="compact-summary"><span class="tool-command">write</span> <span class="tool-path">notes/status.txt</span></span></summary>'
      assert_includes response.body, '<button type="button" class="details-collapse-button" data-collapse-details>▴ Collapse details</button>'
      refute_includes response.body, '<details class="message-details" open>'
      assert_includes response.body, '+71 assert_equal [true, false], messages.map(&amp;:thinking)'
      assert_includes response.body, '545 assert_equal 200, response.status'
      assert_includes response.body, 'Wrote notes/status.txt'
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
      refute_includes response.body, '<details class="message-details" open>'
      assert_includes response.body, 'class="message message--assistant message--compact message--tool-transcript message--tool-error" data-role="assistant"'
      assert_includes response.body, 'Edit 1'
      assert_includes response.body, '- old item'
      assert_includes response.body, '+ new item'
      assert_includes response.body, 'oldText did not match'
      assert_includes response.body, 'read missing.txt'
      assert_includes response.body, 'No such file'
      assert_includes response.body, 'write readonly.txt'
      assert_includes response.body, 'Permission denied'
      assert_includes response.body, '$ false'
      assert_includes response.body, 'Command exited with code 1'
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
      assert_includes response.body, '<summary><span class="compact-summary">$ git status --short (timeout 30s)</span></summary>'
      assert_includes response.body, "$ git status --short (timeout 30s)"
      assert_includes response.body, " M app.rb"
      assert_includes response.body, "Raw details"
      assert_includes response.body, '&quot;type&quot;: &quot;toolCall&quot;'
      assert_includes response.body, '&quot;toolCallId&quot;: &quot;call_123&quot;'
      refute_includes response.body, "[thinking]"
      refute_includes response.body, '<summary><span class="compact-summary">bash</span></summary>'
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
      assert_includes response.body, "Raw details"
      assert_includes response.body, "function renderToolSummary(container, parts, fallback)"
      assert_includes response.body, "message--tool-transcript"
      assert_includes response.body, "toolSummaryParts(toolName, toolPart?.arguments || {})"
      assert_includes response.body, "function transcriptToolCallText(name, args = {})"
      assert_includes response.body, 'if (lines[lines.length - 1] === "") lines.pop();'
      assert_includes response.body, "segment.toolTranscript && segment.error !== true ? segment.text"
      assert_includes response.body, 'details.open = options.open === true;'
      assert_includes response.body, 'collapseButton.textContent = "▴ Collapse details";'
      assert_includes response.body, 'event.target.closest("[data-collapse-details]")'
      assert_includes response.body, 'error: message.isError === true'
      assert_includes response.body, 'open: segment.expanded'
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
      assert_includes response.body, "appendCompactMessage(\"tool\", toolExecutionSummary(event), toolExecutionText(event)"
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
      assert_includes response.body, 'entry.details.open = subagentRunning(event);'
      assert_includes response.body, 'open: event.toolName === "subagent" && subagentRunning(event)'
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
      assert_includes response.body, "requestAnimationFrame(() => requestAnimationFrame"
      assert_includes response.body, "function latestReadableAssistantMessage()"
      assert_includes response.body, "function latestMessageElement()"
      assert_includes response.body, "if (!forceBottomAutoScroll && latestAssistant && latestAssistant === latestMessageElement() && latestAssistant.offsetHeight > conversationScroll.clientHeight)"
      assert_includes response.body, "autoScrollEnabled = nearConversationBottom();"
      assert_includes response.body, "if (autoScrollEnabled && body.closest(\".message\") === latestReadableAssistantMessage()) scheduleAutoScroll();"
      assert_includes response.body, "if (shouldScroll && autoScrollEnabled) scheduleAutoScroll();"
      assert_includes response.body, "applyAutoScroll(\"auto\");"
      assert_includes response.body, "scrollToTop"
      refute_includes response.body, "const turnButton = event.target.closest(\".message-turn-button\");"
      refute_includes response.body, "turnButton.dataset.direction === \"previous\""
      refute_includes response.body, "scrollToUserMessage(target);"
      refute_includes response.body, "function topJumpControlsOffset()"
      refute_includes response.body, "return remSize * 3.5;"
      assert_includes response.body, "function scrollToBottom(behavior = \"auto\", { force = false } = {})"
      assert_includes response.body, "autoScrollEnabled = true;"
      assert_includes response.body, "forceBottomAutoScroll = force;"
      assert_includes response.body, "jumpToLatestButton?.addEventListener(\"click\", () => scrollToBottom(\"auto\", { force: true }));"
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
      assert_includes response.body, "if (sidebarProjectFilterActive() || recentlyInteractedWithSidebar()) {\n        scheduleSidebarRefresh(1000);\n        return;\n      }"
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
      assert_includes response.body, 'appendMessage("assistant", segment.text, true, shouldScroll, timestamp, { thinking: segment.thinking, finalAssistantResponse });'
      assert_includes response.body, 'function renderAssistantMarkdown(body, text, delay = 120)'
      assert_includes response.body, 'body.dataset.rendering = "pending";'
      assert_includes response.body, 'clearTimeout(body.markdownRenderTimeout);'
      assert_includes response.body, 'fetch("/markdown", { method: "POST", body: formData })'
      assert_includes response.body, 'if (["custom", "system", "status"].includes(role)) return "status";'
      assert_includes response.body, "function showStatus(_text, _forceScroll = false) {}"
      assert_includes response.body, "showStatus(eventStatusText(event));"
      assert_includes response.body, "if (/^\\/(?:name|rename)$/.test(trimmed)) return { valid: false };"
      assert_includes response.body, "if (/^\\/(?:name|rename)[ \\t]+[^\\r\\n]+$/.test(trimmed)) return { valid: true };"
      assert_includes response.body, "function sessionNameSlashCommand(message)"
      assert_includes response.body, "function updateSessionHeaderName(name)"
      assert_includes response.body, "function sessionTitleFromEvent(event)"
      assert_includes response.body, "if (event.type === \"session_info\") return event.name;"
      assert_includes response.body, "if (event.type === \"custom\" && event.customType === \"pi-extensions-session-title\") return event.data?.title;"
      assert_includes response.body, "if (event.type === \"custom_message\" && event.customType === \"session-title-update\")"
      assert_includes response.body, "updateSessionHeaderName(sessionTitleFromEvent(event));"
      assert_includes response.body, "function updateHeaderFromSelectedSidebarSession()"
      assert_includes response.body, "const selectedTitle = sessionSidebar?.querySelector(\"a.session.selected .session-title\")?.textContent.trim();"
      assert_includes response.body, "updateHeaderFromSelectedSidebarSession();"
      assert_includes response.body, "const renameCommand = steering ? null : sessionNameSlashCommand(message);"
      assert_includes response.body, "if (!renameCommand) {\n        if (!steering) resetLiveAssistantTracking();\n        resetEventPollBackoff();\n        scheduleNextEventPoll(0);\n        appendMessage(\"user\", [message, pendingImages.length > 0"
      assert_includes response.body, "true, true, new Date(), { optimistic: true, optimisticText: message });"
      assert_includes response.body, "if (payload?.command === \"rename\") {\n          if (payload.error) {\n            setComposerState(\"error\", payload.error);\n            showStatus(payload.error, true);\n            return;\n          }\n          if (payload?.session && promptSessionInput && payload.session !== promptSessionInput.value) {\n            await switchSession(payload.redirect || `/?session=${encodeURIComponent(payload.session)}`, { push: true, focus: true });\n            return;\n          }\n          updateSessionHeaderName(payload.name);\n          setComposerState(\"done\", \"Renamed\");\n          showStatus(eventStatusText({ type: \"session_info\", name: payload.name }), true);\n          refreshSidebar().catch(() => {});\n          return;\n        }"
      assert_includes response.body, "promptForm.requestSubmit();"
      assert_includes response.body, "function resizePromptTextarea()"
      assert_includes response.body, "commandList?.removeAttribute(\"open\");"
      assert_includes response.body, "function filterCommandsFromPrompt()"
      assert_includes response.body, "const query = promptTextarea.value.startsWith(\"/\") ? promptTextarea.value.slice(1).trim().toLowerCase() : \"\";"
      assert_includes response.body, "function selectHighlightedCommand()"
      assert_includes response.body, "setComposerState(\"running\", \"Pi is running…\");"
      assert_includes response.body, "composerStopButton = document.querySelector(\".session-header .composer-stop-button\") || null;"
      assert_includes response.body, "if (!steering) resetLiveAssistantTracking();"
      assert_includes response.body, "const agentBusy = [\"running\", \"sending\"].includes(state);"
      assert_includes response.body, "promptTextarea.disabled = submitting;"
      assert_includes response.body, "composerStopButton.hidden = !agentBusy;"
      assert_includes response.body, "if (steering) formData.set(\"streaming_behavior\", \"steer\");"
      assert_includes response.body, "if (!addImageFiles(files) && composerState?.dataset.state === \"running\") showStatus(\"Steering messages cannot include images\", true);"
      assert_includes response.body, "Send to queue…"
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
      assert_includes response.body, "if (!liveAgentRunning) setComposerState(\"done\", \"Done\");"
      assert_includes response.body, "if (event.type === \"agent_end\") {"
      assert_includes response.body, "liveAgentRunning = false;\n        if (liveAssistantSeen) showStatus(\"Done\");\n        setComposerState(\"done\", \"Done\");"
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
      assert_includes response.body, "navigator.clipboard.writeText"
      assert_includes response.body, "window.isSecureContext"
      assert_includes response.body, "document.execCommand(\"copy\")"
      assert_includes response.body, "Copy failed"
      assert_includes response.body, "empty-state"
      assert_includes response.body, "button:hover"
    end
  end

  private

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

  def project_cwd(root)
    File.join(root, "project")
  end
end

require "minitest/autorun"
require "rack/mock"
require "tmpdir"
require "json"
require "fileutils"
require "base64"
require_relative "../app"

class AppTest < Minitest::Test
  def setup
    PiWebGateway.set :rpc_client_registry, nil
    PiWebGateway.set :pending_rpc_cwds, {}
    PiWebGateway.set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
    PiWebGateway.set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd) }]
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

  def test_json_prompt_redirect_preserves_expanded_sidebar_groups
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
        params: { "session" => path, "message" => "Hello Pi", "expanded_cwd" => [project_cwd(dir)] },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      payload = JSON.parse(response.body)
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(path)
      assert_includes payload.fetch("redirect"), "expanded_cwd"
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
        params: { "session" => path, "expanded_cwd" => [project_cwd(dir)] },
        "HTTP_ACCEPT" => "application/json"
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      payload = JSON.parse(response.body)
      assert_equal new_path, payload.fetch("session")
      assert_includes payload.fetch("redirect"), Rack::Utils.escape(new_path)
      assert_includes payload.fetch("redirect"), "expanded_cwd"
      assert_equal [[ :start_new, project_cwd(dir) ], [ :get_state ]], calls
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
      assert_includes response.body, "command-filter"
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
      assert_includes response.body, "openai-codex/gpt-5.5"
      assert_includes response.body, "medium"
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
      assert_includes response.body, "composer-state"
      refute_includes response.body, "Ready"
      assert_includes response.body, "composer-input-row"
      assert_includes response.body, "Attach images"
      assert_includes response.body, "send-button"
      assert_includes response.body, "Ask Pi… Enter to send, Shift+Enter for newline."
      refute_includes response.body, "autofocus"
      assert_includes response.body, "Abort running Pi"
      refute_includes response.body, "Optional compact instructions"
      refute_includes response.body, ">Compact</button>"
      assert_includes response.body, "nearConversationBottom"
      assert_includes response.body, "jump-to-latest"
      assert_includes response.body, "mobile-sidebar-backdrop"
      assert_includes response.body, "scrollbar-gutter: stable"
      assert_includes response.body, "scrollbar-width: none"
    end
  end

  def test_trims_sidebar_sessions_to_latest_five_by_default
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Show all 7"
      assert_includes response.body, "Session 7"
      assert_includes response.body, "Session 3"
      refute_includes response.body, "Session 2"
      refute_includes response.body, "Session 1"
    end
  end

  def test_keeps_older_selected_session_visible_when_sidebar_is_trimmed
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.first }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Session 1"
      assert_includes response.body, "selected"
    end
  end

  def test_expands_sidebar_cwd_group_to_show_all_sessions
    Dir.mktmpdir do |dir|
      paths = write_sessions(dir, count: 7)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => paths.last, "expanded_cwd" => [project_cwd(dir)] }
      )

      assert_equal 200, response.status
      assert_includes response.body, "Show fewer"
      assert_includes response.body, "Session 7"
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
      assert_includes payload.fetch("sidebar_html"), "expanded_cwd"
      assert_includes payload.fetch("sidebar_html"), "selected"
      assert_includes payload.fetch("conversation_html"), "conversation-panel"
      assert_includes payload.fetch("conversation_html"), "expanded_cwd"
      assert_includes payload.fetch("conversation_html"), paths.first
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
      assert_includes response.body, 'class="message message--status" data-role="system"'
      assert_includes response.body, 'class="message message--status" data-role="custom"'
      assert_includes response.body, 'message--tool'
      assert_includes response.body, 'data-role="toolResult"'
      assert_includes response.body, 'class="message message--error" data-role="error"'
      assert_includes response.body, 'class="message-body"'
      assert_includes response.body, "Hello &lt;Pi&gt;"
      assert_includes response.body, "2026-06-13 10:00"
      refute_includes response.body, "Hello <Pi>"
      assert_includes response.body, "messageRoleKey"
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
      assert_includes response.body, '<details class="message-details" open>'
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
      refute_includes response.body, '<details class="message-details" open>'
      assert_includes response.body, '+71 assert_equal [true, false], messages.map(&amp;:thinking)'
      assert_includes response.body, '545 assert_equal 200, response.status'
      assert_includes response.body, 'Wrote notes/status.txt'
    end
  end

  def test_renders_failed_tool_transcripts_open_with_call_context
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
      assert_includes response.body, '<details class="message-details" open>'
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
      assert_includes response.body, "segment.toolTranscript && !segment.expanded ? segment.text"
      assert_includes response.body, 'details.open = options.open === true;'
      assert_includes response.body, 'open: segment.expanded'
      assert_includes response.body, '["bash", "read", "edit", "write"].includes(segment.toolName)'
      assert_includes response.body, "part.type === \"toolCall\""
      assert_includes response.body, "part.type === \"thinking\""
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
      assert_includes response.body, "let programmaticScroll = false;"
      assert_includes response.body, "function applyAutoScroll(behavior = \"auto\")"
      assert_includes response.body, "requestAnimationFrame(() => requestAnimationFrame"
      assert_includes response.body, "function latestReadableAssistantMessage()"
      assert_includes response.body, "function latestMessageElement()"
      assert_includes response.body, "if (latestAssistant && latestAssistant === latestMessageElement() && latestAssistant.offsetHeight > conversationScroll.clientHeight)"
      assert_includes response.body, "autoScrollEnabled = nearConversationBottom();"
      assert_includes response.body, "if (autoScrollEnabled && body.closest(\".message\") === latestReadableAssistantMessage()) scheduleAutoScroll();"
      assert_includes response.body, "if (shouldScroll && autoScrollEnabled) scheduleAutoScroll();"
      assert_includes response.body, "applyAutoScroll(\"auto\");"
      assert_includes response.body, "autoScrollEnabled = true;"
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
      assert_includes response.body, "if (link.classList.contains(\"selected\")) return;"
      assert_includes response.body, "sessionSidebar.outerHTML = payload.sidebar_html;"
      assert_includes response.body, "conversationPanel.outerHTML = payload.conversation_html;"
      assert_includes response.body, "history.pushState({ session: payload.session }"
      assert_includes response.body, "window.addEventListener(\"popstate\""
      assert_includes response.body, "document.getElementById(\"mobile-session-toggle\").checked = false;"
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
      assert_includes response.body, "lastEventSeq = 0;"
      assert_includes response.body, "autoScrollEnabled = true;"
      assert_includes response.body, "clearAttachments();"
    end
  end

  def test_session_switching_script_intercepts_sidebar_new_sessions
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :rpc_client_factory, [->(_session_path) { FakeRpcClient.new([]) }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_includes response.body, "event.target.closest('.session-sidebar form[action=\"/sessions/new\"]')"
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
      assert_includes response.body, "scheduleNextEventPoll(0);"
      assert_includes response.body, "let eventPollAbortController = null;"
      assert_includes response.body, "const pollTimeout = setTimeout(() => controller.abort(), 12000);"
      assert_includes response.body, "signal: controller.signal"
      assert_includes response.body, "function resumeEventPolling()"
      assert_includes response.body, "abortEventPoll();"
      assert_includes response.body, "window.addEventListener(\"pageshow\", resumeEventPolling);"
      assert_includes response.body, "window.addEventListener(\"focus\", resumeEventPolling);"
      assert_includes response.body, "window.addEventListener(\"online\", resumeEventPolling);"
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
      assert_includes response.body, 'if (roleName === "user") {'
      assert_includes response.body, "function formatTimestamp(timestamp)"
      assert_includes response.body, "function eventTimestamp(event)"
      assert_includes response.body, 'appendMessage("assistant", segment.text, true, shouldScroll, timestamp, { thinking: segment.thinking });'
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
      assert_includes response.body, "const renameCommand = sessionNameSlashCommand(message);"
      assert_includes response.body, "if (!renameCommand) {\n        resetLiveAssistantTracking();\n        resetEventPollBackoff();\n        scheduleNextEventPoll(0);\n        appendMessage(\"user\", [message, pendingImages.length > 0"
      assert_includes response.body, "true, true, new Date());"
      assert_includes response.body, "if (payload?.command === \"rename\") {\n          if (payload.error) {\n            setComposerState(\"error\", payload.error);\n            showStatus(payload.error, true);\n            return;\n          }\n          window.location.href = payload.redirect || window.location.href;\n          return;\n        }"
      assert_includes response.body, "promptForm.requestSubmit();"
      assert_includes response.body, "function resizePromptTextarea()"
      assert_includes response.body, "commandList?.removeAttribute(\"open\");"
      assert_includes response.body, "if (commandFilter) commandFilter.value = \"\";"
      assert_includes response.body, "commandList?.querySelectorAll(\".command\").forEach((command) => { command.hidden = false; });"
      assert_includes response.body, "setComposerState(\"running\", \"Pi is running…\");"
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
      assert_includes response.body, "if ([\"turn_end\", \"agent_end\"].includes(event.type)) {\n        if (liveAssistantSeen) showStatus(\"Done\");\n        setComposerState(\"done\", \"Done\");\n        resetLiveAssistantTracking();"
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

  def project_cwd(root)
    File.join(root, "project")
  end
end

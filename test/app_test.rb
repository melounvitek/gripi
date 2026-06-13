require "minitest/autorun"
require "rack/mock"
require "tmpdir"
require "json"
require "fileutils"
require_relative "../app"

class AppTest < Minitest::Test
  def test_posts_prompt_to_selected_session_and_redirects_back
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
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

  def test_returns_buffered_rpc_events_for_selected_session
    Dir.mktmpdir do |dir|
      path = write_session(dir)
      calls = []
      PiWebGateway.set :sessions_root, dir
      PiWebGateway.set :active_rpc_client, nil
      PiWebGateway.set :active_rpc_session, nil
      PiWebGateway.set :rpc_client_factory, [->(session_path) {
        calls << [:start, session_path]
        FakeRpcClient.new(calls, [{ "type" => "assistant_delta", "text" => "Hi" }])
      }]

      response = Rack::MockRequest.new(PiWebGateway).get(
        "/events",
        params: { "session" => path }
      )

      assert_equal 200, response.status
      assert_equal "application/json", response.content_type
      assert_equal({ "events" => [{ "type" => "assistant_delta", "text" => "Hi" }] }, JSON.parse(response.body))
      assert_equal [[ :start, path ], [ :drain_events ]], calls
    end
  end

  private

  class FakeRpcClient
    def initialize(calls, events = [])
      @calls = calls
      @events = events
    end

    def prompt(message)
      @calls << [:prompt, message]
    end

    def get_messages
      @calls << [:get_messages]
    end

    def drain_events
      @calls << [:drain_events]
      @events
    end

    def close
      @calls << [:close]
    end
  end

  def write_session(root)
    session_dir = File.join(root, "--project--")
    FileUtils.mkdir_p(session_dir)
    path = File.join(session_dir, "session.jsonl")
    File.write(path, JSON.generate({ type: "session", id: "session-1", cwd: "/tmp/project" }) + "\n")
    path
  end
end

require "sinatra/base"
require "erb"
require "json"
require_relative "lib/pi_session_store"
require_relative "lib/pi_rpc_client"

class PiWebGateway < Sinatra::Base
  set :root, File.dirname(__FILE__)
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
  set :active_rpc_client, nil
  set :active_rpc_session, nil

  helpers do
    def h(value)
      ERB::Util.html_escape(value)
    end

    def selected?(session)
      @selected_session&.path == session.path
    end

    def format_time(time)
      time&.strftime("%Y-%m-%d %H:%M") || "unknown"
    end
  end

  get "/" do
    @store = PiSessionStore.new(root: settings.sessions_root)
    @groups = @store.grouped_sessions
    @selected_session = find_selected_session(@groups.values.flatten)
    @messages = @selected_session ? @store.messages(@selected_session.path) : []

    erb :index
  end

  post "/prompt" do
    session_path = params.fetch("session")
    message = params.fetch("message").to_s
    halt 400, "Message cannot be empty" if message.strip.empty?

    client = active_rpc_client(session_path)
    client.prompt(message)
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  get "/events" do
    session_path = params.fetch("session")
    content_type :json
    JSON.generate(events: active_rpc_client(session_path).drain_events)
  end

  private

  def find_selected_session(sessions)
    selected_path = params["session"]
    return sessions.first if selected_path.to_s.empty?

    sessions.find { |session| session.path == selected_path } || sessions.first
  end

  def active_rpc_client(session_path)
    return settings.active_rpc_client if settings.active_rpc_session == session_path && settings.active_rpc_client

    settings.active_rpc_client&.close
    client = settings.rpc_client_factory.first.call(session_path)
    settings.set :active_rpc_session, session_path
    settings.set :active_rpc_client, client
    client
  end
end

require "sinatra/base"
require "erb"
require_relative "lib/pi_session_store"

class PiWebGateway < Sinatra::Base
  set :root, File.dirname(__FILE__)
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))

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

  private

  def find_selected_session(sessions)
    selected_path = params["session"]
    return sessions.first if selected_path.to_s.empty?

    sessions.find { |session| session.path == selected_path } || sessions.first
  end
end

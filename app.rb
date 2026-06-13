require "sinatra/base"
require "erb"
require "json"
require "redcarpet"
require "sanitize"
require_relative "lib/pi_session_store"
require_relative "lib/pi_rpc_client"

class SafeMarkdownRenderer < Redcarpet::Render::HTML
  ALLOWED_MARKDOWN_ELEMENTS = (Sanitize::Config::RELAXED[:elements] + %w[pre code]).uniq.freeze
  ALLOWED_MARKDOWN_ATTRIBUTES = Sanitize::Config::RELAXED[:attributes].merge(
    "a" => (Sanitize::Config::RELAXED[:attributes]["a"] + %w[target rel]).uniq,
    "code" => ["class"]
  ).freeze

  def postprocess(full_document)
    Sanitize.fragment(
      full_document,
      elements: ALLOWED_MARKDOWN_ELEMENTS,
      attributes: ALLOWED_MARKDOWN_ATTRIBUTES,
      protocols: Sanitize::Config::RELAXED[:protocols]
    )
  end
end

class PiWebGateway < Sinatra::Base
  set :root, File.dirname(__FILE__)
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
  set :active_rpc_client, nil
  set :active_rpc_session, nil
  set :active_rpc_cwd, nil

  helpers do
    def h(value)
      ERB::Util.html_escape(value)
    end

    SIDEBAR_SESSION_LIMIT = 5

    def selected?(session)
      @selected_session&.path == session.path
    end

    def visible_sidebar_sessions(cwd, sessions)
      return sessions if expanded_cwd?(cwd)

      visible = sessions.first(SIDEBAR_SESSION_LIMIT)
      if @selected_session&.cwd == cwd && !visible.any? { |session| selected?(session) }
        visible + [@selected_session]
      else
        visible
      end
    end

    def sidebar_group_overflow?(cwd, sessions)
      !expanded_cwd?(cwd) && sessions.length > SIDEBAR_SESSION_LIMIT
    end

    def expanded_cwd?(cwd)
      expanded_cwds.include?(cwd)
    end

    def sidebar_group_url(cwd, expanded:)
      next_expanded_cwds = if expanded
        (expanded_cwds + [cwd]).uniq
      else
        expanded_cwds - [cwd]
      end

      query = {}
      query["session"] = @selected_session.path if @selected_session
      query["expanded_cwd"] = next_expanded_cwds if next_expanded_cwds.any?
      "/?#{Rack::Utils.build_nested_query(query)}"
    end

    def expanded_cwds
      Array(params["expanded_cwd"])
    end

    def format_time(time)
      time&.strftime("%Y-%m-%d %H:%M") || "unknown"
    end

    def message_role_key(role)
      case role.to_s
      when "assistant"
        "assistant"
      when "user"
        "user"
      when "tool", "toolResult"
        "tool"
      when "error"
        "error"
      when "system", "status"
        "status"
      else
        "status"
      end
    end

    def message_role_label(role)
      case role.to_s
      when "toolResult"
        "tool result"
      when "status"
        "status"
      else
        role.to_s.empty? ? "status" : role.to_s
      end
    end

    def message_article_class(message)
      classes = ["message", "message--#{message_role_key(message.role)}"]
      classes << "message--compact" if message.compact
      classes << "message--tool-error" if message.error
      classes.join(" ")
    end

    def message_metadata(message)
      format_time(message.timestamp) if message.timestamp
    end

    def render_message_body(message)
      return h(message.text) unless message.role == "assistant" && !message.compact

      markdown_renderer.render(message.text)
    end

    def markdown_renderer
      @markdown_renderer ||= Redcarpet::Markdown.new(
        SafeMarkdownRenderer.new(
          filter_html: true,
          hard_wrap: true,
          link_attributes: { rel: "nofollow noopener noreferrer", target: "_blank" }
        ),
        autolink: true,
        fenced_code_blocks: true,
        no_intra_emphasis: true,
        space_after_headers: true,
        strikethrough: true,
        tables: true
      )
    end
  end

  get "/" do
    @store = PiSessionStore.new(root: settings.sessions_root)
    @groups = @store.grouped_sessions
    append_pending_active_session(@groups)
    @selected_session = find_selected_session(@groups.values.flatten)
    @messages = @selected_session && File.exist?(@selected_session.path) ? @store.messages(@selected_session.path) : []
    @commands = @selected_session && command_session_available?(@selected_session.path) ? commands_for(@selected_session.path) : []

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

  post "/sessions/new" do
    session_path = params.fetch("session")
    current_session = PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }
    client = active_rpc_client(session_path)
    response = client.new_session(session_path)
    halt 409, "New session was cancelled" if response_cancelled?(response)

    new_session_path = session_file_from(client.get_state) || newest_session_for_same_cwd(session_path)&.path || session_path
    settings.set :active_rpc_cwd, current_session&.cwd
    settings.set :active_rpc_session, new_session_path
    redirect "/?session=#{Rack::Utils.escape(new_session_path)}"
  end

  post "/abort" do
    session_path = params.fetch("session")
    active_rpc_client(session_path).abort
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  post "/compact" do
    session_path = params.fetch("session")
    instructions = params["instructions"].to_s.strip
    active_rpc_client(session_path).compact(instructions.empty? ? nil : instructions)
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  post "/rename" do
    session_path = params.fetch("session")
    name = params.fetch("name").to_s.strip
    halt 400, "Name cannot be empty" if name.empty?

    active_rpc_client(session_path).set_session_name(name)
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  get "/events" do
    session_path = params.fetch("session")
    content_type :json
    events = if session_path == settings.active_rpc_session && settings.active_rpc_client
      settings.active_rpc_client.drain_events
    else
      []
    end
    JSON.generate(events: events)
  end

  private

  def find_selected_session(sessions)
    selected_path = params["session"]
    return sessions.first if selected_path.to_s.empty?

    sessions.find { |session| session.path == selected_path } || sessions.first
  end

  def append_pending_active_session(groups)
    pending_path = params["session"]
    return unless pending_path == settings.active_rpc_session
    return if pending_path.to_s.empty? || File.exist?(pending_path)

    cwd = settings.active_rpc_cwd || "Pending Pi cwd"
    groups[cwd] ||= []
    groups[cwd].unshift(PiSessionStore::Session.new(
      path: pending_path,
      cwd: cwd,
      id: File.basename(pending_path, ".jsonl"),
      display_name: "New session (pending first assistant response)",
      first_user_message: nil,
      message_count: 0,
      created_at: nil,
      modified_at: Time.now
    ))
  end

  def command_session_available?(session_path)
    File.exist?(session_path) || (settings.active_rpc_session == session_path && settings.active_rpc_client)
  end

  def commands_for(session_path)
    response = active_rpc_client(session_path).get_commands
    data = response_data(response)
    data.is_a?(Hash) && data["commands"].is_a?(Array) ? data["commands"] : []
  end

  def active_rpc_client(session_path)
    return settings.active_rpc_client if settings.active_rpc_session == session_path && settings.active_rpc_client

    settings.active_rpc_client&.close
    client = settings.rpc_client_factory.first.call(session_path)
    settings.set :active_rpc_session, session_path
    settings.set :active_rpc_client, client
    client
  end

  def response_data(response)
    response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
  end

  def response_cancelled?(response)
    response_data(response).is_a?(Hash) && response_data(response)["cancelled"]
  end

  def session_file_from(response)
    data = response_data(response)
    return unless data.is_a?(Hash)

    data["sessionFile"] || data["session_file"] || data["path"]
  end

  def newest_session_for_same_cwd(session_path)
    sessions = PiSessionStore.new(root: settings.sessions_root).sessions
    current = sessions.find { |session| session.path == session_path }
    return unless current

    sessions.select { |session| session.cwd == current.cwd && session.path != session_path }.max_by(&:modified_at)
  end
end

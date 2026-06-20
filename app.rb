require "sinatra/base"
require "erb"
require "json"
require_relative "lib/rendering/markdown_renderer"
require_relative "lib/prompts/slash_command"
require_relative "lib/prompts/uploaded_images"
require_relative "lib/rpc/pending_session_registry"
require_relative "lib/rpc/branch_session"
require_relative "lib/rpc/start_new_session"
require_relative "lib/rpc/command_catalog"
require_relative "lib/sessions/sidebar"
require_relative "lib/sessions/session_view"
require "securerandom"
require "ipaddr"
require_relative "lib/pi_session_store"
require_relative "lib/pi_attachment_store"
require_relative "lib/gateway_read_state_store"
require_relative "lib/browser_access_store"
require_relative "lib/pi_rpc_client"
require_relative "lib/pi_rpc_client_registry"
require_relative "lib/time_formatter"

class PiWebGateway < Sinatra::Base
  set :root, File.dirname(__FILE__)
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :attachments_root, ENV.fetch("PI_ATTACHMENTS_ROOT", File.expand_path("~/.pi/web-gateway/attachments"))
  GATEWAY_ENV_PATH = ENV.fetch("PI_GATEWAY_ENV_PATH", File.expand_path("~/.config/pi-web-gateway/env"))

  def self.load_gateway_env_file
    return unless File.exist?(GATEWAY_ENV_PATH)

    File.readlines(GATEWAY_ENV_PATH).each do |line|
      next if line.strip.empty? || line.strip.start_with?("#")

      key, value = line.split("=", 2)
      next unless key && value
      next if ENV.key?(key)

      ENV[key] = value.strip.sub(/\A(['"])(.*)\1\z/, "\\2")
    end
  end

  def self.permitted_hosts_from_env
    ENV.fetch("PI_GATEWAY_PERMITTED_HOSTS", "")
      .split(",")
      .map(&:strip)
      .reject(&:empty?)
  end

  def self.development_permitted_hosts
    [
      "localhost",
      ".localhost",
      ".test",
      IPAddr.new("0.0.0.0/0"),
      IPAddr.new("::/0")
    ]
  end

  load_gateway_env_file
  set :host_authorization, lambda {
    configured_hosts = permitted_hosts_from_env
    if development?
      { permitted_hosts: development_permitted_hosts + configured_hosts }
    elsif configured_hosts.empty?
      {}
    else
      { permitted_hosts: configured_hosts }
    end
  }

  gateway_admin_password = ENV["PI_GATEWAY_ADMIN_PASSWORD"].to_s
  if gateway_admin_password.empty?
    raise "PI_GATEWAY_ADMIN_PASSWORD is required. Set it in #{GATEWAY_ENV_PATH} or in the gateway process environment."
  end

  set :read_state_path, ENV.fetch("PI_READ_STATE_PATH", File.expand_path("~/.pi/web-gateway/read-state.json"))
  set :browser_access_path, ENV.fetch("PI_BROWSER_ACCESS_PATH", File.expand_path("~/.pi/web-gateway/browser-access.json"))
  set :gateway_admin_password, gateway_admin_password
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
  set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd) }]
  set :rpc_client_registry, nil
  set :pending_session_registry, Rpc::PendingSessionRegistry.new
  set :rpc_idle_timeout_seconds, ENV.fetch("PI_RPC_IDLE_TIMEOUT_SECONDS", "1800").to_i

  helpers do
    def h(value)
      ERB::Util.html_escape(value)
    end

    RECENT_SIDEBAR_SESSION_LIMIT = Sessions::Sidebar::RECENT_SESSION_LIMIT
    SIDEBAR_SESSION_PAGE_SIZE = Sessions::Sidebar::SESSION_PAGE_SIZE

    def selected?(session)
      @sidebar.selected?(session)
    end

    def unread?(session)
      @sidebar.unread?(session)
    end

    def session_classes(session, *classes)
      (classes + [selected?(session) ? "selected" : nil, unread?(session) ? "unread" : nil]).compact.join(" ")
    end

    def sorted_sidebar_sessions
      @sidebar.sorted_sessions
    end

    def sidebar_current_session
      @sidebar.current_session
    end

    def unread_sidebar_sessions
      @sidebar.unread_sessions
    end

    def unread_sidebar_session_count
      @sidebar.unread_session_count
    end

    def unread_sidebar_session_count_label
      @sidebar.unread_session_count_label
    end

    def unread_sidebar_session_aria_label
      @sidebar.unread_session_aria_label
    end

    def regular_sidebar_sessions
      @sidebar.regular_sessions
    end

    def regular_sidebar_session_pool
      @sidebar.regular_session_pool
    end

    def recent_sidebar_sessions
      @sidebar.recent_sessions
    end

    def show_all_sidebar_sessions?
      @sidebar.show_all_sessions?
    end

    def sidebar_sessions_limit
      @sidebar.sessions_limit
    end

    def sidebar_sessions_limit_param
      @sidebar.sessions_limit_param
    end

    def sidebar_sessions_overflow?
      @sidebar.sessions_overflow?
    end

    def sidebar_next_sessions_limit
      @sidebar.next_sessions_limit
    end

    def sidebar_sessions_remaining_count
      @sidebar.sessions_remaining_count
    end

    def sidebar_sessions_load_more_url
      @sidebar.sessions_load_more_url
    end

    def known_session_cwds
      @sidebar.known_session_cwds
    end

    def new_session_cwds
      preferred_cwd = selected_project_cwd || @selected_session&.cwd
      return known_session_cwds unless preferred_cwd

      [preferred_cwd, *known_session_cwds.reject { |cwd| cwd == preferred_cwd }]
    end

    def selected_project_cwd
      return @sidebar.selected_project_cwd if defined?(@sidebar) && @sidebar

      project = params["project"].to_s
      project.empty? ? nil : project
    end

    def project_label(session)
      File.basename(session.cwd.to_s)
    end

    def sidebar_session_search_query
      return @sidebar.search_query if defined?(@sidebar) && @sidebar

      params["session_search"].to_s.strip
    end

    def sidebar_session_search?
      return @sidebar.search? if defined?(@sidebar) && @sidebar

      !sidebar_session_search_query.empty?
    end

    def session_matches_sidebar_search?(session)
      @sidebar.matches_search?(session)
    end

    def sidebar_search_clear_url
      @sidebar.search_clear_url
    end

    def session_url(session_path)
      @sidebar.session_url(session_path)
    end

    def session_parent(session)
      session_by_path[normalized_session_path(session&.parent_session_path)]
    end

    def session_children(session)
      children_by_parent_path.fetch(normalized_session_path(session&.path), [])
    end

    def session_child_count(session)
      session_children(session).length
    end

    def session_family_root(session)
      current = session
      seen_paths = {}
      while current && !seen_paths[normalized_session_path(current.path)]
        seen_paths[normalized_session_path(current.path)] = true
        parent = session_parent(current)
        return current unless parent

        current = parent
      end
      current || session
    end

    def session_by_path
      @session_by_path ||= all_sessions.each_with_object({}) { |session, index| index[normalized_session_path(session.path)] = session }
    end

    def children_by_parent_path
      @children_by_parent_path ||= all_sessions.each_with_object(Hash.new { |hash, key| hash[key] = [] }) do |session, children|
        parent_path = normalized_session_path(session.parent_session_path)
        children[parent_path] << session if parent_path && session_by_path.key?(parent_path)
      end.transform_values { |sessions| sessions.sort_by { |session| session.modified_at || Time.at(0) }.reverse }
    end

    def all_sessions
      @all_sessions ||= @groups.values.flatten
    end

    def normalized_session_path(path)
      value = path.to_s
      return if value.empty?

      File.expand_path(value)
    end

    def format_time(time)
      time&.localtime&.strftime("%Y-%m-%d %H:%M") || "unknown"
    end

    def format_relative_time(time)
      TimeFormatter.relative(time)
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
      when "assistant"
        "pi"
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
      classes << "message--thinking" if message.thinking
      classes << "message--tool-transcript" if message.tool_transcript
      classes << "message--tool-error" if message.error
      classes.join(" ")
    end

    def message_metadata(message)
      format_time(message.timestamp) if message.timestamp
    end

    def message_fingerprint(role, text, timestamp)
      timestamp_key = message_timestamp_key(timestamp)
      return unless timestamp_key

      "#{message_role_key(role)}:#{timestamp_key}:#{stable_text_hash(normalized_message_text(text))}"
    end

    def message_timestamp_key(timestamp)
      timestamp&.to_i&.to_s
    end

    def normalized_message_text(text)
      text.to_s.gsub(/\r\n?/, "\n").strip
    end

    def stable_text_hash(text)
      text.bytes.reduce(5381) { |hash, byte| ((hash << 5) + hash + byte) & 0xffffffff }.to_s(16)
    end

    def attachment_label(count)
      "📎 #{count} image attachment#{count == 1 ? "" : "s"}"
    end

    def render_message_body(message)
      return h(message.text) if message.thinking
      return h(message.text) unless message.role == "assistant" && !message.compact

      markdown_renderer.render(message.text)
    end

    def render_compact_message_body(message)
      return h(message.text) unless message.tool_transcript && %w[edit write].include?(message.tool_name)

      message.text.to_s.lines(chomp: true).map do |line|
        %(<span class="tool-diff-line #{h(tool_diff_line_class(line))}">#{h(line)}</span>)
      end.join
    end

    def tool_diff_line_class(line)
      case line
      when /\A\+/
        "tool-diff-line--add"
      when /\A-/
        "tool-diff-line--remove"
      when /\A(?:Edit \d+|write\b|Wrote\b)/
        "tool-diff-line--meta"
      else
        "tool-diff-line--context"
      end
    end

    def session_status_items(status)
      return [] unless status

      [
        ["CTX", format_context_usage(status)],
        ["Model", format_model_with_thinking(status)]
      ].select { |_label, value| value.to_s != "" }
    end

    def format_context_usage(status)
      return if status.context_tokens.nil?

      usage = if status.context_limit
        percent = status.context_percent || ((status.context_tokens.to_f / status.context_limit) * 100).round(1)
        "#{percent}%/#{compact_number(status.context_limit)}"
      else
        compact_number(status.context_tokens)
      end
      status.context_estimated ? "≈#{usage}" : usage
    end

    def format_model(status)
      [status.provider, status.model_id].compact.reject(&:empty?).join("/")
    end

    def format_model_with_thinking(status)
      model = format_model(status)
      return if model.empty?

      [model, status.thinking_level.to_s.empty? ? nil : "(#{status.thinking_level})"].compact.join(" ")
    end

    def compact_number(value)
      number = value.to_f
      return value.to_s if number <= 0
      return number.round.to_s if number < 1_000
      return "#{(number / 1_000).round(1)}k" if number < 1_000_000

      "#{(number / 1_000_000).round(1)}M"
    end

    def markdown_renderer
      @markdown_renderer ||= Redcarpet::Markdown.new(
        Rendering::MarkdownRenderer.new(
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

  before do
    enforce_browser_access
    cleanup_idle_rpc_clients
  end

  post "/browser-access/request" do
    halt 404 unless browser_access_enabled?

    browser_access_store.request_access(browser_token, ip: request.ip, user_agent: request.user_agent)
    redirect safe_return_to
  end

  post "/browser-access/admin-login" do
    halt 404 unless browser_access_enabled?

    if secure_compare(params["password"].to_s, settings.gateway_admin_password.to_s)
      browser_access_store.approve_current_browser(browser_token, label: request.user_agent)
      redirect safe_return_to
    else
      @access_request = browser_access_store.ensure_pending(token: browser_token, ip: request.ip, user_agent: request.user_agent)
      @access_error = "Admin password did not match."
      status 403
      erb :access_blocked
    end
  end

  get "/browser-access/status" do
    halt 404 unless browser_access_enabled?

    content_type :json
    JSON.generate(status: browser_access_store.pending_status(browser_token))
  end

  get "/browser-access/pending" do
    halt 403 unless approved_browser?

    content_type :json
    JSON.generate(requests: browser_access_store.pending_requests)
  end

  post "/browser-access/approve" do
    halt 403 unless approved_browser?

    halt 400, "Code is required" if params["code"].to_s.empty?

    request = browser_access_store.approve_code(params.fetch("code"))
    content_type :json
    JSON.generate(ok: !request.nil?)
  end

  post "/browser-access/deny" do
    halt 403 unless approved_browser?

    halt 400, "Code is required" if params["code"].to_s.empty?

    request = browser_access_store.deny_code(params.fetch("code"))
    content_type :json
    JSON.generate(ok: !request.nil?)
  end

  get "/manifest.webmanifest" do
    content_type "application/manifest+json"
    JSON.generate(
      name: "Pi Web Gateway",
      short_name: "Pi Gateway",
      start_url: "/",
      scope: "/",
      display: "standalone",
      background_color: "#18181e",
      theme_color: "#18181e",
      icons: [
        { src: "/app-icon.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" }
      ]
    )
  end

  get "/app-icon.svg" do
    content_type "image/svg+xml"
    <<~SVG
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512">
        <rect width="512" height="512" rx="112" fill="#18181e"/>
        <circle cx="256" cy="256" r="168" fill="#282832" stroke="#8abeb7" stroke-width="24"/>
        <text x="256" y="296" text-anchor="middle" font-family="system-ui, -apple-system, sans-serif" font-size="148" font-weight="800" fill="#8abeb7">π</text>
      </svg>
    SVG
  end

  get "/service-worker.js" do
    content_type "application/javascript"
    headers "Cache-Control" => "no-cache"
    <<~JS
      self.addEventListener("install", (event) => {
        self.skipWaiting();
      });

      self.addEventListener("activate", (event) => {
        event.waitUntil(self.clients.claim());
      });

      self.addEventListener("message", (event) => {
        const data = event.data || {};
        if (!["pi-notification", "pi-notification-test"].includes(data.type)) return;

        const defaultUrl = data.type === "pi-notification-test" ? "/notification-test" : "/";
        const defaultTag = data.type === "pi-notification-test" ? "pi-notification-test" : "pi-notification";
        event.waitUntil(self.registration.showNotification(data.title || "Pi Web Gateway", {
          body: data.body || "Notifications are working.",
          tag: data.tag || defaultTag,
          renotify: true,
          icon: "/app-icon.svg",
          badge: "/app-icon.svg",
          data: { url: data.url || defaultUrl }
        }));
      });

      self.addEventListener("notificationclick", (event) => {
        event.notification.close();
        const url = event.notification.data?.url || "/";
        event.waitUntil((async () => {
          const clientList = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
          for (const client of clientList) {
            if ("focus" in client) {
              await client.focus();
              if ("navigate" in client) await client.navigate(url);
              return;
            }
          }
          if (self.clients.openWindow) await self.clients.openWindow(url);
        })());
      });
    JS
  end

  get "/notification-test" do
    erb :notification_test
  end

  get "/" do
    prepare_session_view(include_conversation: true)
    erb :index
  end

  get "/sidebar" do
    prepare_session_view
    erb :_sidebar, layout: false
  end

  get "/new_session_modal" do
    prepare_session_view
    erb :_new_session_modal, layout: false
  end

  get "/session_fragment" do
    prepare_session_view(include_conversation: true)
    content_type :json
    JSON.generate(
      url: session_view_url,
      title: @selected_session&.display_name.to_s,
      session: @selected_session&.path,
      sidebar_html: erb(:_sidebar, layout: false),
      conversation_html: erb(:_conversation, layout: false),
      new_session_modal_html: erb(:_new_session_modal, layout: false),
      fork_session_modal_html: erb(:_fork_session_modal, layout: false)
    )
  end

  post "/prompt" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    message = params.fetch("message").to_s
    images = prompt_images_from(params["images"])
    halt 400, "Message cannot be empty" if message.strip.empty? && images.empty?

    steering_prompt = params["streaming_behavior"].to_s == "steer"
    if steering_prompt && !images.empty?
      if json_request?
        status 422
        content_type :json
        next JSON.generate(error: "Steering messages cannot include images")
      end
      halt 422, "Steering messages cannot include images"
    end

    slash_command = steering_prompt ? nil : Prompts::SlashCommand.parse(message)
    branch_response = nil
    if steering_prompt
      with_rpc_client(session_path) { |client| client.steer(message) }
    elsif slash_command&.type == :rename && slash_command.name
      with_rpc_client(session_path) { |client| client.set_session_name(slash_command.name) }
    elsif slash_command&.type == :rename || [:fork, :tree].include?(slash_command&.type)
      nil
    elsif slash_command&.type == :compact
      with_rpc_client(session_path) { |client| client.compact(slash_command.instructions) }
    elsif slash_command&.type == :new
      branch_response = redirect_to_new_session(start_new_session(current_session_cwd(session_path)), command: "new")
    elsif slash_command&.type == :clone
      response = with_rpc_client(session_path) { |client| client.clone_session }
      branch_response = redirect_to_rpc_session_after_branch(session_path, response)
    else
      submitted_at = Time.now
      with_rpc_client(session_path) { |client| client.prompt(message, images) }
      attachment_store.record_prompt(session_path, message, images.length, timestamp: submitted_at)
    end
    redirect_path = session_redirect_path(session_path)
    if branch_response
      branch_response
    elsif json_request?
      content_type :json
      payload = { session: session_path, redirect: redirect_path }
      payload[:steer] = true if steering_prompt && !slash_command
      if slash_command
        payload[:command] = slash_command.type.to_s
        payload[:name] = slash_command.name if slash_command.name
        payload[:error] = slash_command.error if slash_command.error
      end
      JSON.generate(payload)
    else
      redirect redirect_path
    end
  end

  get "/sessions/validate_cwd" do
    result = validated_session_cwd(params["cwd"])
    content_type :json
    if result.fetch(:valid)
      JSON.generate(valid: true, cwd: result.fetch(:cwd))
    else
      status 422
      JSON.generate(valid: false, error: result.fetch(:error))
    end
  end

  post "/sessions/new" do
    session_path = params.fetch("session")
    redirect_to_new_session(start_new_session(current_session_cwd(session_path)))
  end

  post "/sessions/new_at_cwd" do
    result = validated_session_cwd(params["cwd"])
    unless result.fetch(:valid)
      if json_request?
        status 422
        content_type :json
        next JSON.generate(valid: false, error: result.fetch(:error))
      end
      halt 422, result.fetch(:error)
    end

    redirect_to_new_session(start_new_session(result.fetch(:cwd)))
  end

  get "/sessions/fork_messages" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    response = with_rpc_client(session_path) { |client| client.get_fork_messages }
    messages = response_data(response).fetch("messages", [])
    content_type :json
    JSON.generate(messages: messages)
  end

  get "/sessions/tree_entries" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    current_leaf_id = with_rpc_client(session_path) { |client| client.tree_leaf }
    store = PiSessionStore.new(root: settings.sessions_root)
    entries = store.tree_entries(session_path, current_leaf_id: current_leaf_id)
    content_type :json
    JSON.generate(entries: entries)
  end

  post "/sessions/tree" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    entry_id = params.fetch("entry_id").to_s
    halt 400, "Tree entry cannot be empty" if entry_id.empty?

    response = with_rpc_client(session_path) { |client| client.navigate_tree(entry_id) }
    data = response_data(response)
    payload = { session: session_path, redirect: session_redirect_path(session_path), cancelled: data.is_a?(Hash) ? data["cancelled"] || false : false }
    content_type :json
    JSON.generate(payload)
  end

  post "/sessions/fork" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    entry_id = params.fetch("entry_id").to_s
    halt 400, "Fork entry cannot be empty" if entry_id.empty?

    response = with_rpc_client(session_path) { |client| client.fork(entry_id) }
    redirect_to_rpc_session_after_branch(session_path, response, text: response_data(response)["text"])
  end

  post "/sessions/clone" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    response = with_rpc_client(session_path) { |client| client.clone_session }
    redirect_to_rpc_session_after_branch(session_path, response)
  end

  post "/abort" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    with_rpc_client(session_path) { |client| client.abort }
    if json_request?
      content_type :json
      JSON.generate(ok: true, session: session_path)
    else
      redirect session_redirect_path(session_path)
    end
  end

  post "/compact" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    instructions = params["instructions"].to_s.strip
    with_rpc_client(session_path) { |client| client.compact(instructions.empty? ? nil : instructions) }
    redirect session_redirect_path(session_path)
  end

  post "/rename" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    name = params.fetch("name").to_s.strip
    halt 400, "Name cannot be empty" if name.empty?

    with_rpc_client(session_path) { |client| client.set_session_name(name) }
    redirect session_redirect_path(session_path)
  end

  get "/events" do
    session_path = params.fetch("session")
    after_seq = params.fetch("after", 0).to_i
    content_type :json
    JSON.generate(rpc_clients.events_after(session_path, after_seq))
  end

  get "/status" do
    session_path = params.fetch("session")
    halt 404 unless File.exist?(session_path)

    content_type :json
    status = PiSessionStore.new(root: settings.sessions_root).status(session_path)
    JSON.generate(
      context: format_context_usage(status),
      model: format_model(status),
      thinking: status.thinking_level
    )
  end

  get "/commands" do
    session_path = params.fetch("session")
    halt 404 unless command_session_available?(session_path)

    @commands = commands_for(session_path)
    erb :_commands, layout: false
  end

  post "/markdown" do
    content_type :json
    JSON.generate(html: markdown_renderer.render(params.fetch("text").to_s))
  end

  private

  ACCESS_ENDPOINTS = %w[
    /browser-access/request
    /browser-access/admin-login
    /browser-access/status
    /browser-access/pending
    /browser-access/approve
    /browser-access/deny
  ].freeze
  BROWSER_ACCESS_STORE_CACHE = {}
  BROWSER_ACCESS_STORE_CACHE_MUTEX = Mutex.new

  def browser_access_enabled?
    !settings.gateway_admin_password.to_s.empty?
  end

  def browser_access_store
    path = settings.browser_access_path
    BROWSER_ACCESS_STORE_CACHE_MUTEX.synchronize do
      BROWSER_ACCESS_STORE_CACHE[path] ||= BrowserAccessStore.new(path: path)
    end
  end

  def browser_token
    return @browser_token if defined?(@browser_token)

    @browser_token = request.cookies["pi_gateway_browser"]
    return @browser_token unless @browser_token.to_s.empty?

    @browser_token = SecureRandom.hex(32)
    response.set_cookie("pi_gateway_browser", value: @browser_token, path: "/", httponly: true, same_site: :lax, max_age: 365 * 24 * 60 * 60)
    @browser_token
  end

  def approved_browser?
    browser_access_enabled? && browser_access_store.approved?(browser_token)
  end

  def enforce_browser_access
    return unless browser_access_enabled?
    return if ACCESS_ENDPOINTS.include?(request.path_info)
    return if approved_browser?

    @access_request = browser_access_store.ensure_pending(token: browser_token, ip: request.ip, user_agent: request.user_agent)
    @access_error = nil
    status 403
    halt erb(:access_blocked)
  end

  def safe_return_to
    return_to = params["return_to"].to_s
    return return_to if return_to.start_with?("/") && !return_to.start_with?("//")

    "/"
  end

  def secure_compare(left, right)
    return false if left.empty? || right.empty?
    return false unless left.bytesize == right.bytesize

    Rack::Utils.secure_compare(left, right)
  end

  def json_request?
    request.env["HTTP_ACCEPT"].to_s.include?("application/json")
  end

  def redirect_to_new_session(new_session_path, command: nil)
    redirect_path = session_redirect_path(new_session_path)
    if json_request?
      content_type :json
      payload = { session: new_session_path, redirect: redirect_path }
      payload[:command] = command if command
      JSON.generate(payload)
    else
      redirect redirect_path
    end
  end

  def current_session_cwd(session_path)
    current_session = PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }
    current_session&.cwd || pending_rpc_cwd(session_path) || File.dirname(session_path)
  end

  def start_new_session(cwd)
    Rpc::StartNewSession.call(
      cwd,
      client_factory: settings.new_rpc_client_factory.first,
      rpc_clients: rpc_clients,
      pending_sessions: pending_session_registry,
      sessions_root: settings.sessions_root
    )
  end

  def redirect_to_rpc_session_after_branch(previous_session_path, response, text: nil)
    data = response_data(response)
    if data.is_a?(Hash) && data["cancelled"]
      status 409 if json_request?
      content_type :json if json_request?
      return JSON.generate(cancelled: true, session: previous_session_path) if json_request?

      redirect session_redirect_path(previous_session_path)
    end

    new_session_path = branch_session_path(previous_session_path)
    redirect_path = session_redirect_path(new_session_path)
    if json_request?
      content_type :json
      payload = { session: new_session_path, redirect: redirect_path }
      payload[:text] = text if text
      JSON.generate(payload)
    else
      redirect redirect_path
    end
  end

  def branch_session_path(previous_session_path)
    Rpc::BranchSession.call(
      previous_session_path,
      rpc_clients: rpc_clients,
      pending_sessions: pending_session_registry,
      cwd: branched_session_cwd(previous_session_path)
    )
  end

  def branched_session_cwd(previous_session_path)
    session_cwd(previous_session_path) || pending_rpc_cwd(previous_session_path) || File.dirname(previous_session_path)
  end

  def validated_session_cwd(raw_cwd)
    cwd = raw_cwd.to_s.strip
    return { valid: false, error: "Enter an existing directory." } if cwd.empty?

    expanded_cwd = File.expand_path(cwd)
    return { valid: false, error: "Path must be an existing directory." } unless File.directory?(expanded_cwd)
    return { valid: false, error: "Directory is not accessible." } unless File.readable?(expanded_cwd) && File.executable?(expanded_cwd)

    { valid: true, cwd: File.realpath(expanded_cwd) }
  rescue ArgumentError, Errno::ENOENT, Errno::EACCES
    { valid: false, error: "Path must be an existing directory." }
  end

  def attachment_store
    PiAttachmentStore.new(root: settings.attachments_root)
  end

  def read_state_store
    if @read_state_store_path != settings.read_state_path
      @read_state_store_path = settings.read_state_path
      @read_state_store = GatewayReadStateStore.new(path: settings.read_state_path)
    end
    @read_state_store
  end

  def prepare_session_view(include_conversation: false)
    remap_selected_pending_session
    Sessions::SessionView.build(
      sessions_root: settings.sessions_root,
      params: params,
      include_conversation: include_conversation,
      read_state_store: read_state_store,
      attachment_store: attachment_store,
      rpc_clients: rpc_clients,
      mark_selected_read: should_mark_selected_session_read?,
      pending_session_cwd: ->(path) { pending_rpc_cwd(path) }
    ).to_instance_variables.each do |name, value|
      instance_variable_set(name, value)
    end
  end

  def should_mark_selected_session_read?
    request.path_info != "/sidebar" || !params["session"].to_s.empty?
  end

  def remap_selected_pending_session
    selected_path = params["session"]
    return if selected_path.to_s.empty?

    real_path = remap_active_pending_rpc_client(selected_path)
    params["session"] = real_path if real_path
  end

  def session_view_url
    query = {}
    query["session"] = @selected_session.path if @selected_session
    query["project"] = selected_project_cwd if selected_project_cwd
    query["session_search"] = sidebar_session_search_query if sidebar_session_search?
    "/?#{Rack::Utils.build_nested_query(query)}"
  end

  def session_redirect_path(session_path)
    query = { "session" => session_path }
    query["project"] = selected_project_cwd if selected_project_cwd
    query["session_search"] = sidebar_session_search_query if sidebar_session_search?
    "/?#{Rack::Utils.build_nested_query(query)}"
  end

  def prompt_images_from(upload_param)
    Prompts::UploadedImages.parse(upload_param)
  rescue Prompts::UploadedImages::ValidationError => error
    halt 400, error.message
  end

  def command_session_available?(session_path)
    rpc_clients.active?(session_path) || known_session_path?(session_path)
  end

  def known_session_path?(session_path)
    PiSessionStore.new(root: settings.sessions_root).sessions.any? { |session| session.path == session_path }
  end

  def commands_for(session_path)
    response = with_rpc_client(session_path) { |client| client.get_commands }
    Rpc::CommandCatalog.commands_from(response)
  rescue Errno::EPIPE, IOError
    Rpc::CommandCatalog.builtin_commands
  end

  def rpc_clients
    settings.rpc_client_registry ||= PiRpcClientRegistry.new(factory: settings.rpc_client_factory.first)
  end

  def cleanup_idle_rpc_clients
    timeout = settings.rpc_idle_timeout_seconds
    return unless timeout.positive?

    rpc_clients.close_idle_clients(
      idle_timeout: timeout,
      except: pending_rpc_cwd_paths
    )
  end

  def with_rpc_client(session_path)
    session_path = canonical_rpc_session_path(session_path)
    rpc_clients.with_client(session_path) { |client| yield client }
  end

  def canonical_rpc_session_path(session_path)
    remapped_path = remap_active_pending_rpc_client(session_path)
    return remapped_path if remapped_path

    remap_pending_rpc_client(session_path) unless rpc_clients.active?(session_path)
    session_path
  end

  def remap_active_pending_rpc_client(session_path)
    cwd = pending_rpc_cwd(session_path)
    return unless cwd && rpc_clients.active?(session_path)

    real_path = session_file_from(rpc_clients.client_for(session_path).get_state)
    return unless real_path && File.exist?(real_path) && session_cwd(real_path) == cwd

    rpc_clients.move(session_path, real_path)
    forget_pending_rpc_cwd(session_path)
    real_path
  end

  def remap_pending_rpc_client(session_path)
    return unless File.exist?(session_path)

    pending_path = matching_pending_rpc_path(session_path)
    return unless pending_path

    rpc_clients.move(pending_path, session_path)
    forget_pending_rpc_cwd(pending_path)
  end

  def matching_pending_rpc_path(session_path)
    pending_rpc_cwd_entries.find do |pending_path, cwd|
      next unless rpc_clients.active?(pending_path)
      next unless session_cwd(session_path) == cwd

      session_file_from(rpc_clients.client_for(pending_path).get_state) == session_path
    end&.first
  end

  def pending_rpc_cwd(session_path)
    pending_session_registry.cwd_for(session_path)
  end

  def pending_rpc_cwd_paths
    pending_session_registry.paths
  end

  def pending_rpc_cwd_entries
    pending_session_registry.entries
  end

  def forget_pending_rpc_cwd(session_path)
    pending_session_registry.forget(session_path)
  end

  def pending_session_registry
    settings.pending_session_registry
  end

  def session_cwd(session_path)
    PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }&.cwd
  end

  def response_data(response)
    response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
  end

  def session_file_from(response)
    data = response_data(response)
    return unless data.is_a?(Hash)

    data["sessionFile"] || data["session_file"] || data["path"]
  end

end

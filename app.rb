require "sinatra/base"
require "erb"
require "json"
require "base64"
require "redcarpet"
require "nokogiri"
require "sanitize"
require "securerandom"
require "set"
require_relative "lib/pi_session_store"
require_relative "lib/pi_attachment_store"
require_relative "lib/gateway_read_state_store"
require_relative "lib/pi_rpc_client"
require_relative "lib/pi_rpc_client_registry"

class SafeMarkdownRenderer < Redcarpet::Render::HTML
  ALLOWED_MARKDOWN_ELEMENTS = (Sanitize::Config::RELAXED[:elements] + %w[pre code]).uniq.freeze
  ALLOWED_MARKDOWN_ATTRIBUTES = Sanitize::Config::RELAXED[:attributes].merge(
    "a" => (Sanitize::Config::RELAXED[:attributes]["a"] + %w[target rel]).uniq,
    "code" => ["class"],
    "ol" => (Sanitize::Config::RELAXED[:attributes]["ol"] + %w[start]).uniq
  ).freeze

  def postprocess(full_document)
    Sanitize.fragment(
      continue_ordered_lists(full_document),
      elements: ALLOWED_MARKDOWN_ELEMENTS,
      attributes: ALLOWED_MARKDOWN_ATTRIBUTES,
      protocols: Sanitize::Config::RELAXED[:protocols]
    )
  end

  private

  def continue_ordered_lists(full_document)
    fragment = Nokogiri::HTML5.fragment(full_document)
    next_ordered_list_start = nil

    fragment.children.each do |node|
      if whitespace_text?(node)
        next
      elsif code_block?(node)
        next
      elsif ordered_list?(node)
        item_count = node.element_children.count { |child| child.name == "li" }
        node["start"] = next_ordered_list_start.to_s if next_ordered_list_start
        next_ordered_list_start = (next_ordered_list_start || 1) + item_count
      else
        next_ordered_list_start = nil
      end
    end

    fragment.to_html
  end

  def ordered_list?(node)
    node.element? && node.name == "ol"
  end

  def code_block?(node)
    node.element? && node.name == "pre"
  end

  def whitespace_text?(node)
    node.text? && node.text.strip.empty?
  end
end

class PiWebGateway < Sinatra::Base
  MAX_PROMPT_IMAGES = 5
  MAX_PROMPT_IMAGE_BYTES = 10 * 1024 * 1024

  set :root, File.dirname(__FILE__)
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :attachments_root, ENV.fetch("PI_ATTACHMENTS_ROOT", File.expand_path("~/.pi/web-gateway/attachments"))
  set :read_state_path, ENV.fetch("PI_READ_STATE_PATH", File.expand_path("~/.pi/web-gateway/read-state.json"))
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path) }]
  set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd) }]
  set :rpc_client_registry, nil
  set :pending_rpc_cwds, {}
  set :pending_rpc_cwds_mutex, Mutex.new
  set :rpc_idle_timeout_seconds, ENV.fetch("PI_RPC_IDLE_TIMEOUT_SECONDS", "1800").to_i

  helpers do
    def h(value)
      ERB::Util.html_escape(value)
    end

    SIDEBAR_SESSION_LIMIT = 5
    RECENT_SIDEBAR_SESSION_LIMIT = 9

    def selected?(session)
      @selected_session&.path == session.path
    end

    def unread?(session)
      !selected?(session) && read_state_store.unread?(session)
    end

    def session_classes(session, *classes)
      (classes + [selected?(session) ? "selected" : nil, unread?(session) ? "unread" : nil]).compact.join(" ")
    end

    def visible_sidebar_sessions(cwd, sessions)
      return sessions if expanded_cwd?(cwd)

      project_sessions = sessions.reject { |session| recent_sidebar_session_paths.include?(session.path) }
      visible = project_sessions.first(SIDEBAR_SESSION_LIMIT)
      if @selected_session&.cwd == cwd && !recent_sidebar_session_paths.include?(@selected_session.path) && !visible.any? { |session| selected?(session) }
        visible + [@selected_session]
      else
        visible
      end
    end

    def recent_sidebar_sessions
      @recent_sidebar_sessions ||= @groups.values.flatten.sort_by { |session| session.modified_at || Time.at(0) }.reverse.first(RECENT_SIDEBAR_SESSION_LIMIT)
    end

    def recent_sidebar_session_paths
      @recent_sidebar_session_paths ||= recent_sidebar_sessions.map(&:path).to_set
    end

    def project_label(session)
      File.basename(session.cwd.to_s)
    end

    def sidebar_group_overflow?(cwd, sessions)
      !expanded_cwd?(cwd) && sessions.length > SIDEBAR_SESSION_LIMIT
    end

    def expanded_cwd?(cwd)
      expanded_cwds.include?(cwd)
    end

    def session_url(session_path)
      query = { "session" => session_path }
      query["expanded_cwd"] = expanded_cwds if expanded_cwds.any?
      "/?#{Rack::Utils.build_nested_query(query)}"
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
      time&.localtime&.strftime("%Y-%m-%d %H:%M") || "unknown"
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

    def session_status_items(status)
      return [] unless status

      [
        ["CTX", format_context_usage(status)],
        ["Model", format_model_with_thinking(status)]
      ].select { |_label, value| value.to_s != "" }
    end

    def format_context_usage(status)
      return if status.context_tokens.nil?

      if status.context_limit
        percent = status.context_percent || ((status.context_tokens.to_f / status.context_limit) * 100).round(1)
        "#{percent}%/#{compact_number(status.context_limit)}"
      else
        compact_number(status.context_tokens)
      end
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

  before do
    cleanup_idle_rpc_clients
  end

  get "/" do
    prepare_session_view
    erb :index
  end

  get "/sidebar" do
    prepare_session_view
    erb :_sidebar, layout: false
  end

  get "/session_fragment" do
    prepare_session_view
    content_type :json
    JSON.generate(
      url: session_view_url,
      title: @selected_session&.display_name.to_s,
      session: @selected_session&.path,
      sidebar_html: erb(:_sidebar, layout: false),
      conversation_html: erb(:_conversation, layout: false)
    )
  end

  post "/prompt" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    message = params.fetch("message").to_s
    images = prompt_images_from(params["images"])
    halt 400, "Message cannot be empty" if message.strip.empty? && images.empty?

    rename_command = session_name_slash_command(message)
    if rename_command&.[](:name)
      with_rpc_client(session_path) { |client| client.set_session_name(rename_command.fetch(:name)) }
    elsif rename_command
      nil
    else
      submitted_at = Time.now
      with_rpc_client(session_path) { |client| client.prompt(message, images) }
      attachment_store.record_prompt(session_path, message, images.length, timestamp: submitted_at)
    end
    redirect_path = session_redirect_path(session_path, expanded_cwds: expanded_cwds)
    if json_request?
      content_type :json
      payload = { session: session_path, redirect: redirect_path }
      if rename_command
        payload[:command] = "rename"
        payload[:name] = rename_command[:name] if rename_command[:name]
        payload[:error] = rename_command.fetch(:error) if rename_command[:error]
      end
      JSON.generate(payload)
    else
      redirect redirect_path
    end
  end

  post "/sessions/new" do
    session_path = params.fetch("session")
    current_session = PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }
    cwd = current_session&.cwd || File.dirname(session_path)
    client = settings.new_rpc_client_factory.first.call(cwd)
    new_session_path = session_file_from(client.get_state) || pending_session_path(cwd)
    rpc_clients.register(new_session_path, client)
    remember_pending_rpc_cwd(new_session_path, cwd) unless File.exist?(new_session_path)
    redirect_path = session_redirect_path(new_session_path, expanded_cwds: expanded_cwds)
    if json_request?
      content_type :json
      JSON.generate(session: new_session_path, redirect: redirect_path)
    else
      redirect redirect_path
    end
  end

  post "/abort" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    with_rpc_client(session_path) { |client| client.abort }
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  post "/compact" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    instructions = params["instructions"].to_s.strip
    with_rpc_client(session_path) { |client| client.compact(instructions.empty? ? nil : instructions) }
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
  end

  post "/rename" do
    session_path = canonical_rpc_session_path(params.fetch("session"))
    name = params.fetch("name").to_s.strip
    halt 400, "Name cannot be empty" if name.empty?

    with_rpc_client(session_path) { |client| client.set_session_name(name) }
    redirect "/?session=#{Rack::Utils.escape(session_path)}"
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

  def json_request?
    request.env["HTTP_ACCEPT"].to_s.include?("application/json")
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

  def prepare_session_view
    @store = PiSessionStore.new(root: settings.sessions_root, delete_missing_cwds: true)
    @groups = @store.grouped_sessions
    append_pending_active_session(@groups)
    all_sessions = @groups.values.flatten
    read_state_store.observe_sessions(all_sessions)
    @selected_session = find_selected_session(all_sessions)
    read_state_store.mark_read(@selected_session) if @selected_session && should_mark_selected_session_read?
    @messages = @selected_session && File.exist?(@selected_session.path) ? @store.messages(@selected_session.path) : []
    @attachment_counts = @selected_session && File.exist?(@selected_session.path) ? attachment_store.counts_for_messages(@selected_session.path, @messages) : {}
    @session_status = @selected_session && File.exist?(@selected_session.path) ? @store.status(@selected_session.path) : nil
  end

  def should_mark_selected_session_read?
    request.path_info != "/sidebar" || !params["session"].to_s.empty?
  end

  def session_view_url
    query = {}
    query["session"] = @selected_session.path if @selected_session
    query["expanded_cwd"] = expanded_cwds if expanded_cwds.any?
    "/?#{Rack::Utils.build_nested_query(query)}"
  end

  def session_name_slash_command(message)
    match = message.strip.match(%r{\A/(name|rename)(?:[ \t]+([^\r\n]+))?\z})
    return nil unless match

    name = match[2]&.strip
    name ? { name: name } : { error: "Usage: /#{match[1]} <name>" }
  end

  def session_redirect_path(session_path, expanded_cwds: [])
    query = { "session" => session_path }
    query["expanded_cwd"] = expanded_cwds if expanded_cwds.any?
    "/?#{Rack::Utils.build_nested_query(query)}"
  end

  def prompt_images_from(upload_param)
    uploads = Array(upload_param).compact
    halt 400, "Too many images" if uploads.length > MAX_PROMPT_IMAGES

    uploads.map do |upload|
      tempfile = uploaded_tempfile(upload)
      mime_type = uploaded_content_type(upload).to_s
      halt 400, "Only image uploads are supported" unless tempfile && mime_type.start_with?("image/")
      halt 400, "Image upload is too large" if tempfile.size > MAX_PROMPT_IMAGE_BYTES

      tempfile.rewind if tempfile.respond_to?(:rewind)
      { type: "image", data: Base64.strict_encode64(tempfile.read), mimeType: mime_type }
    end
  end

  def uploaded_tempfile(upload)
    return upload.tempfile if upload.respond_to?(:tempfile)
    return File.open(upload.path, "rb") if upload.respond_to?(:path)
    return upload[:tempfile] if upload.is_a?(Hash) && upload.key?(:tempfile)

    upload["tempfile"] if upload.is_a?(Hash)
  end

  def uploaded_content_type(upload)
    return upload.content_type if upload.respond_to?(:content_type)
    return upload[:type] if upload.is_a?(Hash) && upload.key?(:type)

    upload["type"] if upload.is_a?(Hash)
  end

  def find_selected_session(sessions)
    selected_path = params["session"]
    return sessions.first if selected_path.to_s.empty?

    sessions.find { |session| session.path == selected_path } || sessions.first
  end

  def append_pending_active_session(groups)
    pending_path = params["session"]
    return if pending_path.to_s.empty? || File.exist?(pending_path)

    cwd = pending_rpc_cwd(pending_path)
    return unless cwd
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
    rpc_clients.active?(session_path) || known_session_path?(session_path)
  end

  def known_session_path?(session_path)
    PiSessionStore.new(root: settings.sessions_root).sessions.any? { |session| session.path == session_path }
  end

  def commands_for(session_path)
    response = with_rpc_client(session_path) { |client| client.get_commands }
    data = response_data(response)
    data.is_a?(Hash) && data["commands"].is_a?(Array) ? data["commands"] : []
  rescue Errno::EPIPE, IOError
    []
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

  def remember_pending_rpc_cwd(session_path, cwd)
    settings.pending_rpc_cwds_mutex.synchronize do
      settings.pending_rpc_cwds[session_path] = cwd
    end
  end

  def pending_rpc_cwd(session_path)
    settings.pending_rpc_cwds_mutex.synchronize do
      settings.pending_rpc_cwds[session_path]
    end
  end

  def pending_rpc_cwd_paths
    settings.pending_rpc_cwds_mutex.synchronize do
      settings.pending_rpc_cwds.keys
    end
  end

  def pending_rpc_cwd_entries
    settings.pending_rpc_cwds_mutex.synchronize do
      settings.pending_rpc_cwds.to_a
    end
  end

  def forget_pending_rpc_cwd(session_path)
    settings.pending_rpc_cwds_mutex.synchronize do
      settings.pending_rpc_cwds.delete(session_path)
    end
  end

  def session_cwd(session_path)
    PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }&.cwd
  end

  def pending_session_path(cwd)
    File.join(settings.sessions_root, "pending-#{SecureRandom.uuid}.jsonl")
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

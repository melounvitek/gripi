require "erb"
require_relative "../rendering/markdown_renderer"
require_relative "../sessions/session_view"
require_relative "../sessions/sidebar"
require_relative "../time_formatter"

module Web
  module ViewHelpers
    RAW_DETAILS_INLINE_BYTE_LIMIT = 8 * 1024

    def h(value)
      ERB::Util.html_escape(value)
    end

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
      @session_family.parent(session)
    end

    def session_children(session)
      @session_family.children(session)
    end

    def session_child_count(session)
      @session_family.child_count(session)
    end

    def session_family_root(session)
      @session_family.root(session)
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
        %(<span class="tool-diff-line #{h(tool_diff_line_class(line, message.tool_preview))}">#{h(line)}</span>)
      end.join
    end

    def defer_raw_details?(message)
      message.raw_details.to_s.bytesize > RAW_DETAILS_INLINE_BYTE_LIMIT
    end

    def raw_details_url(message_index, message)
      query = {
        "session" => @selected_session&.path || params["session"].to_s,
        "message_index" => message_index,
        "raw_details_token" => Sessions::SessionView.raw_details_token_for(message)
      }
      "/message_raw_details?#{Rack::Utils.build_nested_query(query)}"
    end

    def tool_diff_line_class(line, preview = false)
      return "tool-diff-line--meta tool-diff-line--preview-heading" if preview && line.match?(/\AEdit \d+/)

      case line
      when /\A\+/
        ["tool-diff-line--add", ("tool-diff-line--preview-ellipsis" if preview && line == "+ …")].compact.join(" ")
      when /\A-/
        ["tool-diff-line--remove", ("tool-diff-line--preview-ellipsis" if preview && line == "- …")].compact.join(" ")
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
end

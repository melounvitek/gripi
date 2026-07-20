require "base64"
require "digest"
require "erb"
require_relative "../rendering/markdown_renderer"
require_relative "../sessions/sidebar"
require_relative "../configured_session_cwds"
require_relative "../time_formatter"

module Web
  module ViewHelpers
    TOOL_OUTPUT_DESKTOP_TAIL_LINES = 18
    TOOL_OUTPUT_MOBILE_TAIL_LINES = 12
    TERMINAL_OUTPUT_MAX_SOURCE_BYTES = 262_144
    TERMINAL_OUTPUT_EXCLUDED_TOOLS = %w[read edit write].freeze
    PROJECT_IDENTITY_COLORS = [
      ["#6a3b1d33", "#e6a66f"],
      ["#334f7833", "#8db9ef"],
      ["#563a7033", "#c5a0e8"],
      ["#70374633", "#ef9aae"],
      ["#4b612b33", "#acd276"],
      ["#285d7033", "#75c5df"],
      ["#67365f33", "#dfa0d4"],
      ["#66502033", "#e0bd65"],
      ["#315d3b33", "#86cb98"],
      ["#3f477533", "#a5afe9"],
      ["#713f3233", "#eda18b"],
      ["#215f5933", "#76cbbf"]
    ].freeze

    def h(value)
      ERB::Util.html_escape(value)
    end

    def selected?(session)
      @sidebar.selected?(session)
    end

    def unread?(session)
      @sidebar.unread?(session)
    end

    def pinned?(session)
      @sidebar.pinned?(session)
    end

    def session_classes(session, *classes)
      (classes + [selected?(session) ? "selected" : nil, unread?(session) ? "unread" : nil]).compact.join(" ")
    end

    def sorted_sidebar_sessions
      @sidebar.sorted_sessions
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

    def sidebar_sessions
      @sidebar.sessions
    end

    def sidebar_pinned_sessions
      @sidebar.pinned_sessions
    end

    def sidebar_separate_current_session
      @sidebar.separate_current_session
    end

    def sidebar_session_pool
      @sidebar.session_pool
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
      cwds = [*known_session_cwds, *configured_session_cwds].uniq
      return cwds unless preferred_cwd

      [preferred_cwd, *cwds.reject { |cwd| cwd == preferred_cwd }]
    end

    def configured_session_cwds
      @configured_session_cwds ||= ConfiguredSessionCwds.read(settings.session_cwds_path)
    end

    def new_session_cwd_label(cwd)
      basename = File.basename(cwd.to_s)
      duplicate = new_session_cwds.count { |known_cwd| File.basename(known_cwd.to_s) == basename } > 1
      duplicate ? "#{basename} — #{cwd}" : basename
    end

    def selected_project_cwd
      return @sidebar.selected_project_cwd if defined?(@sidebar) && @sidebar

      project = params["project"].to_s
      project.empty? ? nil : project
    end

    def project_label(session)
      File.basename(session.cwd.to_s)
    end

    def project_identity(cwd)
      label = File.basename(cwd.to_s).unicode_normalize(:nfc)
      words = label.scan(/[[:alnum:]]+/)
      monogram = if words.length > 1
        words.first(2).map { |word| word.upcase.scan(/\X/).first }.join
      elsif words.any?
        words.first.upcase.scan(/\X/).first(2).join
      else
        label.upcase.scan(/\X/).first(2).join
      end
      background, foreground = PROJECT_IDENTITY_COLORS[Digest::SHA256.digest(label).getbyte(0) % PROJECT_IDENTITY_COLORS.length]

      { monogram: monogram, background: background, foreground: foreground }
    end

    def sidebar_session_search_query
      return @sidebar.search_query if defined?(@sidebar) && @sidebar

      params["session_search"].to_s.strip
    end

    def sidebar_session_search?
      return @sidebar.search? if defined?(@sidebar) && @sidebar

      !sidebar_session_search_query.empty?
    end

    def sidebar_filters?
      return @sidebar.filters? if defined?(@sidebar) && @sidebar

      sidebar_session_search? || !!selected_project_cwd
    end

    def session_matches_sidebar_filters?(session)
      @sidebar.matches_filters?(session)
    end

    def sidebar_filters_clear_url
      @sidebar.filters_clear_url
    end

    def session_url(session_path)
      return session_only_url(session_path) if session_only?

      @sidebar.session_url(session_path)
    end

    def session_only?
      params["session_only"].to_s == "1"
    end

    def session_only_url(session_path)
      "/?#{Rack::Utils.build_nested_query("session" => session_path, "session_only" => "1")}"
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
      when "tool", "toolResult", "bashExecution"
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
      when "bashExecution"
        "shell"
      when "status"
        "status"
      else
        role.to_s.empty? ? "status" : role.to_s
      end
    end

    def message_display_role_label(message)
      return "tool" if message.compact && message.role == "assistant" && !message.tool_name.to_s.empty?
      return "[#{message.custom_type}]" if message.role == "custom" && !message.custom_type.to_s.empty?

      message_role_label(message.role)
    end

    def message_article_class(message)
      classes = ["message", "message--#{message_role_key(message.role)}"]
      classes << "message--compact" if message.compact
      classes << "message--compaction" if message.compaction
      classes << "message--bash-execution" if message.role == "bashExecution"
      classes << "message--bash-excluded" if message.bash_excluded_from_context
      classes << "message--bash-cancelled" if message.bash_cancelled
      classes << "message--bash-truncated" if message.bash_truncated
      classes << "message--thinking" if message.thinking
      classes << "message--tool-call" if message.compact && message.role == "assistant" && !message.tool_name.to_s.empty?
      classes << "message--tool-transcript" if message.tool_transcript
      classes << "message--tool-error" if message.error
      classes.join(" ")
    end

    def message_metadata(message)
      format_time(message.timestamp) if message.timestamp
    end

    def persisted_active_bash_message(active_bash, messages)
      return unless active_bash

      summary = "$ #{display_home_path(active_bash[:command] || active_bash["command"])}"
      started_at = active_bash[:started_at] || active_bash["started_at"]
      Array(messages).reverse_each.find do |message|
        message.role == "bashExecution" && message.summary == summary && message.timestamp && started_at && message.timestamp.to_f >= started_at.to_f
      end
    end

    def completed_bash_event_persisted?(event, messages)
      return false unless event["type"] == "bash_end"

      result = event["result"]
      return false unless result.is_a?(Hash)

      started_at = event["startedAt"]
      return false unless started_at.is_a?(Numeric)

      summary = "$ #{display_home_path(event["command"])}"
      started_at /= 1000.0
      Array(messages).reverse_each.any? do |message|
        message.role == "bashExecution" &&
          message.summary == summary &&
          message.text == result["output"].to_s &&
          message.bash_exit_code == result["exitCode"] &&
          message.bash_cancelled == (result["cancelled"] == true) &&
          message.bash_truncated == (result["truncated"] == true) &&
          message.bash_excluded_from_context == (event["excludeFromContext"] == true) &&
          message.bash_recorded_at && message.bash_recorded_at.to_f >= started_at
      end
    end

    def partition_completed_bash_events(events, messages)
      persisted = []
      remaining = Array(events).reject do |event|
        next false unless completed_bash_event_persisted?(event, messages)

        persisted << event["bashId"]
        true
      end
      [remaining, persisted]
    end

    def bash_execution_status_items(message)
      items = []
      items << "excluded from model context" if message.bash_excluded_from_context
      items << "exit #{message.bash_exit_code}" if message.bash_exit_code.is_a?(Integer) && !message.bash_exit_code.zero?
      items << "cancelled" if message.bash_cancelled
      items << "output truncated" if message.bash_truncated
      items << "full output: #{message.bash_full_output_path}" if message.bash_truncated && !message.bash_full_output_path.to_s.empty?
      items
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
      return markdown_renderer.render(message.text) if message.thinking
      return h(message.text) unless ["assistant", "custom"].include?(message.role) && !message.compact

      markdown_renderer.render(message.text)
    end

    def render_message_images(images)
      Array(images).filter_map do |image|
        src = image[:src] || image["src"] || data_image_src(image)
        next if src.to_s.empty?

        %(<img class="message-image" src="#{h(src)}" alt="Attached image" loading="lazy" decoding="async">)
      end.join
    end

    def data_image_src(image)
      mime_type = image[:mime_type] || image["mime_type"] || image[:mimeType] || image["mimeType"]
      data = image[:data] || image["data"]
      return unless %w[image/png image/jpeg image/gif image/webp].include?(mime_type)
      return if data.to_s.empty?

      "data:#{mime_type};base64,#{data}"
    end

    def render_compact_message_body(message)
      render_compact_message_lines(message, tool_output_lines(message), 0)
    end

    def terminal_tool_output?(message)
      return false unless message.compact && !TERMINAL_OUTPUT_EXCLUDED_TOOLS.include?(message.tool_name)

      text = message.text.to_s
      text.match?(/[\x08\x1b\u0080-\u009f]/) || text.match?(/\r(?!\n)/)
    end

    def terminal_tool_output_source(message)
      Base64.strict_encode64(bounded_terminal_tool_output_text(message))
    end

    def terminal_tool_output_truncated?(message)
      message.text.to_s.bytesize > TERMINAL_OUTPUT_MAX_SOURCE_BYTES
    end

    def bounded_terminal_tool_output_text(message)
      text = message.text.to_s
      return text unless terminal_tool_output_truncated?(message)

      latest_screen = text.rindex("\e[2J")
      suffix = text[latest_screen..] if latest_screen
      return suffix if suffix && suffix.bytesize <= TERMINAL_OUTPUT_MAX_SOURCE_BYTES

      text.byteslice(-TERMINAL_OUTPUT_MAX_SOURCE_BYTES, TERMINAL_OUTPUT_MAX_SOURCE_BYTES).scrub
    end

    def collapsible_tool_output?(message)
      return false unless %w[assistant tool toolResult bashExecution].include?(message.role)
      return false unless message.compact && !message.thinking && !message.final_assistant_response

      tool_output_lines(message).length > TOOL_OUTPUT_DESKTOP_TAIL_LINES
    end

    def tool_output_lines(message)
      message.text.to_s.lines(chomp: true)
    end

    def display_tool_output_line(message, line)
      return line if message.tool_preview

      display_home_path(line)
    end

    def display_home_path(text)
      home = Dir.home
      text.to_s.gsub(/(?<![A-Za-z0-9_.~\/-])#{Regexp.escape(home)}(?=\/|\z|[^A-Za-z0-9_.~\/-])/, "~")
    end

    def tool_output_hidden_line_count(message, tail_lines)
      [tool_output_lines(message).length - tail_lines, 0].max
    end

    def render_tool_output_tail(message)
      tail_lines = tool_output_lines(message).last(TOOL_OUTPUT_DESKTOP_TAIL_LINES) || []
      desktop_extra_count = [tail_lines.length - TOOL_OUTPUT_MOBILE_TAIL_LINES, 0].max
      render_compact_message_lines(message, tail_lines, desktop_extra_count)
    end

    def render_compact_message_lines(message, lines, desktop_only_count)
      diff_output = message.tool_transcript && %w[edit write].include?(message.tool_name)
      rendered_lines = if diff_output
        lines.map.with_index do |line, index|
          classes = ["tool-diff-line", tool_diff_line_class(line, message.tool_preview)]
          classes << "tool-output-tail-desktop-extra" if index < desktop_only_count
          %(<span class="#{h(classes.join(" "))}">#{h(display_tool_output_line(message, line))}</span>)
        end
      else
        lines.map.with_index do |line, index|
          classes = ["tool-output-line"]
          classes << "tool-output-tail-desktop-extra" if index < desktop_only_count
          %(<span class="#{h(classes.join(" "))}">#{h(display_tool_output_line(message, line))}</span>)
        end
      end

      classes = ["tool-output-content", ("tool-output-content--diff" if diff_output)].compact
      %(<span class="#{classes.join(" ")}">#{rendered_lines.join}</span>)
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
      return if status.context_tokens.nil? && status.context_limit.nil?

      usage = if status.context_limit
        percent = status.context_tokens.nil? ? "?" : (status.context_percent || (status.context_tokens.to_f / status.context_limit) * 100).round(1)
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

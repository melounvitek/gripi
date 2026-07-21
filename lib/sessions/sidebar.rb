require "rack/utils"

module Sessions
  class Sidebar
    RECENT_SESSION_LIMIT = 20
    SESSION_PAGE_SIZE = 20

    def initialize(groups:, selected_session:, params:, read_state_store:, pinned_session_store: nil)
      @groups = groups
      @selected_session = selected_session
      @params = params
      @unread_paths = read_state_store.unread_paths(@groups.values.flatten).to_h { |path| [path, true] }
      @pinned_paths = pinned_session_store&.pinned_paths&.to_h { |path| [path, true] } || {}
    end

    def selected?(session)
      @selected_session&.path == session.path
    end

    def unread?(session)
      !selected?(session) && @unread_paths.key?(session.path)
    end

    def pinned?(session)
      session && @pinned_paths.key?(session.path)
    end

    def pinned_sessions
      @pinned_sessions ||= sorted_sessions.select { |session| pinned?(session) }
    end

    def sorted_sessions
      @sorted_sessions ||= @groups.values.flatten.sort_by { |session| session.conversation_activity_at || Time.at(0) }.reverse
    end

    def unread_session_count
      @unread_session_count ||= sorted_sessions.count { |session| unread?(session) }
    end

    def unread_session_count_label
      unread_session_count > 99 ? "99+" : unread_session_count.to_s
    end

    def unread_session_aria_label
      count = unread_session_count
      "#{count} unread #{count == 1 ? "session" : "sessions"}"
    end

    def sessions
      @sessions ||= session_pool.first(sessions_limit)
    end

    def separate_current_session
      @selected_session unless pinned?(@selected_session) || sessions.any? { |session| selected?(session) }
    end

    def session_pool
      @session_pool ||= sorted_sessions.reject { |session| pinned?(session) }.select { |session| matches_filters?(session) }
    end

    def show_all_sessions?
      @params["show_all_sessions"] == "1"
    end

    def sessions_limit
      return session_pool.length if show_all_sessions?

      [@params["sidebar_sessions_limit"].to_i, RECENT_SESSION_LIMIT].max
    end

    def sessions_limit_param
      return nil if show_all_sessions? || sessions_limit <= RECENT_SESSION_LIMIT

      sessions_limit.to_s
    end

    def retained_sessions_limit_for(session_path)
      limit = sessions_limit_param
      return unless limit

      index = session_pool.index { |session| session.path == session_path }
      limit if index && index >= RECENT_SESSION_LIMIT
    end

    def sessions_overflow?
      sessions_limit < session_pool.length
    end

    def next_sessions_limit
      [sessions_limit + SESSION_PAGE_SIZE, session_pool.length].min
    end

    def sessions_remaining_count
      session_pool.length - [sessions_limit, session_pool.length].min
    end

    def sessions_load_more_url
      url_for(sidebar_sessions_limit: next_sessions_limit.to_s)
    end

    def known_session_cwds
      @known_session_cwds ||= @groups.keys.sort_by do |cwd|
        latest = @groups.fetch(cwd).map { |session| session.conversation_activity_at || Time.at(0) }.max || Time.at(0)
        [-latest.to_f, File.basename(cwd).downcase]
      end
    end

    def selected_project_cwd
      project = @params["project"].to_s
      return if project.empty?
      return project if @groups.key?(project)
    end

    def search_query
      @params["session_search"].to_s.strip
    end

    def search?
      !search_query.empty?
    end

    def filters?
      search? || !!selected_project_cwd
    end

    def conversation_search_query_for(session)
      query = search_query
      return if query.length < 3
      return unless session.first_user_message.to_s.downcase.include?(query.downcase)

      query
    end

    def matches_filters?(session)
      (!selected_project_cwd || session.cwd == selected_project_cwd) && matches_search?(session)
    end

    def matches_search?(session)
      query = search_query.downcase
      return true if query.empty?

      [session.display_name, session.cwd, project_label(session), session.first_user_message].any? do |value|
        value.to_s.downcase.include?(query)
      end
    end

    def filters_clear_url
      url_for(include_filters: false)
    end

    def session_url(session_path)
      url_for(session: session_path, sidebar_sessions_limit: retained_sessions_limit_for(session_path))
    end

    private

    def project_label(session)
      File.basename(session.cwd.to_s)
    end

    def url_for(session: @selected_session&.path, include_filters: true, include_search: include_filters, sidebar_sessions_limit: nil)
      query = {}
      query["session"] = session if session
      query["project"] = selected_project_cwd if include_filters && selected_project_cwd
      query["session_search"] = search_query if include_search && search?
      query["sidebar_sessions_limit"] = sidebar_sessions_limit if sidebar_sessions_limit
      "/?#{Rack::Utils.build_nested_query(query)}"
    end
  end
end

require "rack/utils"

module Sessions
  class Sidebar
    RECENT_SESSION_LIMIT = 20
    SESSION_PAGE_SIZE = 20

    attr_reader :current_session

    def initialize(groups:, selected_session:, params:, read_state_store:)
      @groups = groups
      @selected_session = selected_session
      @params = params
      @read_state_store = read_state_store
      @current_session = selected_session
    end

    def selected?(session)
      @selected_session&.path == session.path
    end

    def unread?(session)
      !selected?(session) && @read_state_store.unread?(session)
    end

    def sorted_sessions
      @sorted_sessions ||= @groups.values.flatten.sort_by { |session| session.modified_at || Time.at(0) }.reverse
    end

    def unread_sessions
      @unread_sessions ||= sorted_sessions.reject { |session| selected?(session) }.select { |session| unread?(session) && matches_search?(session) }
    end

    def unread_session_count
      unread_sessions.length
    end

    def unread_session_count_label
      unread_session_count > 99 ? "99+" : unread_session_count.to_s
    end

    def unread_session_aria_label
      count = unread_session_count
      "#{count} unread #{count == 1 ? "session" : "sessions"}"
    end

    def regular_sessions
      @regular_sessions ||= regular_session_pool.first(sessions_limit)
    end

    def regular_session_pool
      @regular_session_pool ||= begin
        sessions = sorted_sessions.reject { |session| selected?(session) || unread?(session) }
        sessions = sessions.select { |session| session.cwd == selected_project_cwd } if selected_project_cwd
        sessions.select { |session| matches_search?(session) }
      end
    end

    def recent_sessions
      [current_session, *unread_sessions, *regular_sessions].compact
    end

    def show_all_sessions?
      @params["show_all_sessions"] == "1"
    end

    def sessions_limit
      return regular_session_pool.length if show_all_sessions?

      [@params["sidebar_sessions_limit"].to_i, RECENT_SESSION_LIMIT].max
    end

    def sessions_limit_param
      return nil if show_all_sessions? || sessions_limit <= RECENT_SESSION_LIMIT

      sessions_limit.to_s
    end

    def sessions_overflow?
      regular_sessions.length < regular_session_pool.length
    end

    def next_sessions_limit
      [sessions_limit + SESSION_PAGE_SIZE, regular_session_pool.length].min
    end

    def sessions_remaining_count
      regular_session_pool.length - regular_sessions.length
    end

    def sessions_load_more_url
      url_for(sidebar_sessions_limit: next_sessions_limit.to_s)
    end

    def known_session_cwds
      @known_session_cwds ||= @groups.keys.sort_by do |cwd|
        latest = @groups.fetch(cwd).map { |session| session.modified_at || Time.at(0) }.max || Time.at(0)
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

    def matches_search?(session)
      query = search_query.downcase
      return true if query.empty?

      [session.display_name, session.cwd, project_label(session), session.first_user_message].any? do |value|
        value.to_s.downcase.include?(query)
      end
    end

    def search_clear_url
      url_for(include_search: false)
    end

    def session_url(session_path)
      url_for(session: session_path)
    end

    private

    def project_label(session)
      File.basename(session.cwd.to_s)
    end

    def url_for(session: @selected_session&.path, include_search: true, sidebar_sessions_limit: nil)
      query = {}
      query["session"] = session if session
      query["project"] = selected_project_cwd if selected_project_cwd
      query["session_search"] = search_query if include_search && search?
      query["sidebar_sessions_limit"] = sidebar_sessions_limit if sidebar_sessions_limit
      "/?#{Rack::Utils.build_nested_query(query)}"
    end
  end
end

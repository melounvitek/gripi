require "minitest/autorun"
require "set"
require_relative "../lib/sessions/sidebar"

class SessionsSidebarTest < Minitest::Test
  Session = Struct.new(:path, :cwd, :display_name, :first_user_message, :modified_at, :conversation_activity_at, keyword_init: true)

  def test_loads_unread_state_once_for_all_sessions
    sessions = 3.times.map { |index| session("session-#{index}", "project", "Session #{index}", index) }
    read_state = FakeReadState.new([sessions.first.path, sessions.last.path])

    sidebar = Sessions::Sidebar.new(
      groups: groups(*sessions),
      selected_session: sessions.first,
      params: {},
      read_state_store: read_state,
      pinned_session_store: FakePinnedSessions.new([])
    )

    refute sidebar.unread?(sessions.first)
    assert sidebar.unread?(sessions.last)
    assert_equal 1, sidebar.unread_session_count
    assert_equal [sessions], read_state.unread_path_calls
  end

  def test_keeps_filtered_sessions_strict_while_exposing_filtered_out_current_session
    current = session("current", "current-project", "Current work", 10)
    unread = session("unread", "unread-project", "Unread work", 30)
    matching = session("matching", "filtered-project", "Matching work", 20, first_user_message: "Webhook setup")
    hidden = session("hidden", "other-project", "Hidden work", 40)
    sidebar = build_sidebar(
      groups: groups(current, unread, matching, hidden),
      selected_session: current,
      params: { "project" => matching.cwd, "session_search" => "work" },
      unread_paths: [unread.path]
    )

    assert_equal [hidden.path, unread.path, matching.path, current.path], sidebar.sorted_sessions.map(&:path)
    assert_equal [matching], sidebar.sessions
    assert_equal current, sidebar.separate_current_session
    assert_equal 1, sidebar.unread_session_count
  end

  def test_keeps_pinned_sessions_visible_above_filtered_regular_sessions
    pinned = session("pinned", "pinned-project", "Pinned", 10)
    matching = session("matching", "matching-project", "Matching", 20)
    hidden = session("hidden", "hidden-project", "Hidden", 30)
    sidebar = build_sidebar(
      groups: groups(pinned, matching, hidden),
      selected_session: matching,
      params: { "project" => matching.cwd },
      pinned_paths: [pinned.path]
    )

    assert_equal [pinned], sidebar.pinned_sessions
    assert_equal [matching], sidebar.sessions
    refute_includes sidebar.session_pool, pinned
  end

  def test_pinned_sessions_do_not_consume_the_recent_session_limit
    pinned = session("pinned", "project", "Pinned", -1)
    regulars = 21.times.map { |index| session("regular-#{index}", "project", "Regular #{index}", index) }
    sidebar = build_sidebar(
      groups: groups(pinned, *regulars),
      selected_session: regulars.last,
      pinned_paths: [pinned.path]
    )

    assert_equal [pinned], sidebar.pinned_sessions
    assert_equal 20, sidebar.sessions.length
    assert sidebar.sessions_overflow?
  end

  def test_does_not_separate_a_pinned_current_session
    current = session("current", "project", "Current", 1)
    sidebar = build_sidebar(groups: groups(current), selected_session: current, pinned_paths: [current.path])

    assert_nil sidebar.separate_current_session
  end

  def test_orders_sessions_by_conversation_activity_instead_of_file_modification
    newer_conversation = session("newer-conversation", "project", "Newer conversation", 20, modified_at: 10)
    newer_file = session("newer-file", "project", "Newer file", 10, modified_at: 30)

    sidebar = build_sidebar(groups: groups(newer_file, newer_conversation), selected_session: newer_file)

    assert_equal [newer_conversation, newer_file], sidebar.sorted_sessions
  end

  def test_paginates_filtered_sessions_including_matching_current_session_in_counts
    current = session("current", "project", "Current regular", -1)
    regulars = 25.times.map { |index| session("regular-#{index}", "project", "Regular #{index}", index) }
    sidebar = build_sidebar(
      groups: groups(current, *regulars),
      selected_session: current,
      params: { "project" => current.cwd, "session_search" => "regular" },
      unread_paths: [regulars.last.path]
    )

    assert_equal 20, sidebar.sessions.length
    assert_includes sidebar.sessions, regulars.last
    assert sidebar.unread?(regulars.last)
    assert_equal 1, sidebar.unread_session_count
    refute_includes sidebar.sessions, current
    assert_equal current, sidebar.separate_current_session
    assert sidebar.sessions_overflow?
    assert_equal 26, sidebar.next_sessions_limit
    assert_equal 6, sidebar.sessions_remaining_count
    assert_includes sidebar.sessions_load_more_url, "session=#{Rack::Utils.escape(current.path)}"
    assert_includes sidebar.sessions_load_more_url, "project=project"
    assert_includes sidebar.sessions_load_more_url, "session_search=regular"
    assert_includes sidebar.sessions_load_more_url, "sidebar_sessions_limit=26"
  end

  def test_moves_current_session_into_chronological_position_when_page_reaches_it
    current = session("current", "project", "Current", 4.5)
    sessions = 25.times.map { |index| session("session-#{index}", "project", "Session #{index}", index) }

    first_page = build_sidebar(groups: groups(current, *sessions), selected_session: current)
    loaded_page = build_sidebar(
      groups: groups(current, *sessions),
      selected_session: current,
      params: { "sidebar_sessions_limit" => "25" }
    )

    assert_equal current, first_page.separate_current_session
    refute_includes first_page.sessions, current
    assert_nil loaded_page.separate_current_session
    assert_equal current, loaded_page.sessions[-5]
    assert_equal 25, loaded_page.sessions.length
  end

  def test_session_urls_retain_loaded_limit_only_beyond_the_first_page
    sessions = 25.times.map { |index| session("session-#{index}", "project", "Session #{index}", index) }
    sidebar = build_sidebar(
      groups: groups(*sessions),
      selected_session: sessions.last,
      params: { "sidebar_sessions_limit" => "25" }
    )

    refute_includes sidebar.session_url(sessions.last.path), "sidebar_sessions_limit"
    assert_includes sidebar.session_url(sessions.first.path), "sidebar_sessions_limit=25"
  end

  def test_lists_known_projects_by_recent_activity_then_name
    alpha = session("alpha", "alpha-project", "Alpha", 10, modified_at: 30)
    beta = session("beta", "beta-project", "Beta", 20, modified_at: 10)
    gamma = session("gamma", "gamma-project", "Gamma", 20, modified_at: 20)
    sidebar = build_sidebar(groups: groups(alpha, beta, gamma), selected_session: alpha)

    assert_equal ["beta-project", "gamma-project", "alpha-project"], sidebar.known_session_cwds
  end

  def test_exposes_only_known_conversation_search_matches_for_handoff
    matching = session("matching", "project", "Matching", 20, first_user_message: "Investigate Webhook delivery")
    metadata_only = session("metadata", "webhook-project", "Metadata only", 10, first_user_message: "Unrelated question")
    sidebar = build_sidebar(
      groups: groups(matching, metadata_only),
      selected_session: metadata_only,
      params: { "session_search" => "webhook" }
    )

    assert_equal "webhook", sidebar.conversation_search_query_for(matching)
    assert_nil sidebar.conversation_search_query_for(metadata_only)
  end

  def test_does_not_handoff_short_conversation_search_queries
    matching = session("matching", "project", "Matching", 20, first_user_message: "Investigate Webhook delivery")
    sidebar = build_sidebar(
      groups: groups(matching),
      selected_session: matching,
      params: { "session_search" => "we" }
    )

    assert_nil sidebar.conversation_search_query_for(matching)
  end

  def test_clear_filters_url_removes_search_and_project_but_keeps_session
    current = session("current", "current-project", "Current", 20)
    filtered = session("filtered", "filtered-project", "Filtered", 10)
    sidebar = build_sidebar(
      groups: groups(current, filtered),
      selected_session: current,
      params: { "project" => filtered.cwd, "session_search" => "work" }
    )

    assert sidebar.filters?
    assert_equal "/?session=#{Rack::Utils.escape(current.path)}", sidebar.filters_clear_url
  end

  private

  def build_sidebar(groups:, selected_session:, params: {}, unread_paths: [], pinned_paths: [])
    Sessions::Sidebar.new(
      groups: groups,
      selected_session: selected_session,
      params: params,
      read_state_store: FakeReadState.new(unread_paths),
      pinned_session_store: FakePinnedSessions.new(pinned_paths)
    )
  end

  def groups(*sessions)
    sessions.group_by(&:cwd)
  end

  def session(id, cwd, name, activity_at, first_user_message: nil, modified_at: activity_at)
    Session.new(
      path: "/sessions/#{id}.jsonl",
      cwd: cwd,
      display_name: name,
      first_user_message: first_user_message,
      modified_at: Time.at(modified_at),
      conversation_activity_at: Time.at(activity_at)
    )
  end

  class FakePinnedSessions
    def initialize(paths)
      @paths = paths.to_set
    end

    def pinned_paths
      @paths.to_a
    end
  end

  class FakeReadState
    attr_reader :unread_path_calls

    def initialize(unread_paths)
      @unread_paths = unread_paths.to_set
      @unread_path_calls = []
    end

    def unread_paths(sessions)
      @unread_path_calls << sessions
      sessions.filter_map { |session| session.path if @unread_paths.include?(session.path) }
    end
  end
end

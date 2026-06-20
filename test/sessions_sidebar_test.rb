require "minitest/autorun"
require "set"
require_relative "../lib/sessions/sidebar"

class SessionsSidebarTest < Minitest::Test
  Session = Struct.new(:path, :cwd, :display_name, :first_user_message, :modified_at, keyword_init: true)

  def test_groups_current_unread_and_filtered_regular_sessions
    current = session("current", "current-project", "Current work", 40)
    unread = session("unread", "unread-project", "Unread work", 30)
    matching = session("matching", "filtered-project", "Matching work", 20, first_user_message: "Webhook setup")
    hidden = session("hidden", "other-project", "Hidden work", 50)
    sidebar = build_sidebar(
      groups: groups(current, unread, matching, hidden),
      selected_session: current,
      params: { "project" => matching.cwd, "session_search" => "work" },
      unread_paths: [unread.path]
    )

    assert_equal [hidden.path, current.path, unread.path, matching.path], sidebar.sorted_sessions.map(&:path)
    assert_equal [unread], sidebar.unread_sessions
    assert_equal [matching], sidebar.regular_sessions
    assert_equal [current, unread, matching], sidebar.recent_sessions
  end

  def test_paginates_regular_sessions_and_builds_load_more_url
    current = session("current", "project", "Current", 100)
    regulars = 25.times.map { |index| session("regular-#{index}", "project", "Regular #{index}", index) }
    sidebar = build_sidebar(
      groups: groups(current, *regulars),
      selected_session: current,
      params: { "project" => current.cwd, "session_search" => "regular" }
    )

    assert_equal 20, sidebar.regular_sessions.length
    assert sidebar.sessions_overflow?
    assert_equal 25, sidebar.next_sessions_limit
    assert_equal 5, sidebar.sessions_remaining_count
    assert_includes sidebar.sessions_load_more_url, "session=#{Rack::Utils.escape(current.path)}"
    assert_includes sidebar.sessions_load_more_url, "project=project"
    assert_includes sidebar.sessions_load_more_url, "session_search=regular"
    assert_includes sidebar.sessions_load_more_url, "sidebar_sessions_limit=25"
  end

  def test_lists_known_projects_by_recent_activity_then_name
    alpha = session("alpha", "alpha-project", "Alpha", 10)
    beta = session("beta", "beta-project", "Beta", 20)
    gamma = session("gamma", "gamma-project", "Gamma", 20)
    sidebar = build_sidebar(groups: groups(alpha, beta, gamma), selected_session: alpha)

    assert_equal ["beta-project", "gamma-project", "alpha-project"], sidebar.known_session_cwds
  end

  private

  def build_sidebar(groups:, selected_session:, params: {}, unread_paths: [])
    Sessions::Sidebar.new(
      groups: groups,
      selected_session: selected_session,
      params: params,
      read_state_store: FakeReadState.new(unread_paths)
    )
  end

  def groups(*sessions)
    sessions.group_by(&:cwd)
  end

  def session(id, cwd, name, modified_at, first_user_message: nil)
    Session.new(
      path: "/sessions/#{id}.jsonl",
      cwd: cwd,
      display_name: name,
      first_user_message: first_user_message,
      modified_at: Time.at(modified_at)
    )
  end

  class FakeReadState
    def initialize(unread_paths)
      @unread_paths = unread_paths.to_set
    end

    def unread?(session)
      @unread_paths.include?(session.path)
    end
  end
end

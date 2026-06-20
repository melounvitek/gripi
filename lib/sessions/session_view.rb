require_relative "../pi_session_store"
require_relative "sidebar"

module Sessions
  class SessionView
    PENDING_SESSION_DISPLAY_NAME = "New session (pending first assistant response)"

    attr_reader :store,
      :groups,
      :all_sessions,
      :selected_session,
      :sidebar,
      :current_tree_leaf_known,
      :latest_tree_leaf_id,
      :viewing_older_tree_leaf,
      :messages,
      :attachment_counts,
      :session_status

    def self.build(**kwargs)
      new(**kwargs).build
    end

    def initialize(
      sessions_root:,
      params:,
      include_conversation:,
      read_state_store:,
      attachment_store:,
      rpc_clients:,
      mark_selected_read:,
      pending_session_cwd:,
      now: Time.now
    )
      @sessions_root = sessions_root
      @params = params
      @include_conversation = include_conversation
      @read_state_store = read_state_store
      @attachment_store = attachment_store
      @rpc_clients = rpc_clients
      @mark_selected_read = mark_selected_read
      @pending_session_cwd = pending_session_cwd
      @now = now
    end

    def build
      @store = PiSessionStore.new(root: @sessions_root, delete_missing_cwds: true)
      @groups = @store.grouped_sessions
      append_pending_active_session
      @all_sessions = @groups.values.flatten
      @read_state_store.observe_sessions(@all_sessions)
      @selected_session = find_selected_session
      @read_state_store.mark_read(@selected_session) if @selected_session && @mark_selected_read
      @sidebar = Sessions::Sidebar.new(
        groups: @groups,
        selected_session: @selected_session,
        params: @params,
        read_state_store: @read_state_store
      )
      prepare_conversation
      self
    end

    def to_instance_variables
      {
        :@store => @store,
        :@groups => @groups,
        :@all_sessions => @all_sessions,
        :@selected_session => @selected_session,
        :@sidebar => @sidebar,
        :@current_tree_leaf_known => @current_tree_leaf_known,
        :@latest_tree_leaf_id => @latest_tree_leaf_id,
        :@viewing_older_tree_leaf => @viewing_older_tree_leaf,
        :@messages => @messages,
        :@attachment_counts => @attachment_counts,
        :@session_status => @session_status
      }
    end

    private

    def append_pending_active_session
      pending_path = selected_session_path
      return if pending_path.empty? || File.exist?(pending_path)

      cwd = @pending_session_cwd.call(pending_path)
      return unless cwd

      @groups[cwd] ||= []
      @groups[cwd].unshift(PiSessionStore::Session.new(
        path: pending_path,
        cwd: cwd,
        id: File.basename(pending_path, ".jsonl"),
        display_name: PENDING_SESSION_DISPLAY_NAME,
        first_user_message: nil,
        message_count: 0,
        created_at: nil,
        modified_at: @now
      ))
    end

    def selected_session_path
      @params["session"].to_s
    end

    def find_selected_session
      return @all_sessions.first if selected_session_path.empty?

      @all_sessions.find { |session| session.path == selected_session_path } || @all_sessions.first
    end

    def prepare_conversation
      existing_session = selected_existing_session?
      existing_conversation = @include_conversation && existing_session
      current_leaf_id = current_leaf_id_for(existing_conversation)

      @latest_tree_leaf_id = existing_conversation ? @store.latest_leaf_id(@selected_session.path) : nil
      @viewing_older_tree_leaf = @current_tree_leaf_known && current_leaf_id != @latest_tree_leaf_id
      @messages = existing_session ? @store.messages(@selected_session.path, current_leaf_id: current_leaf_id) : []
      @attachment_counts = existing_conversation ? @attachment_store.counts_for_messages(@selected_session.path, @messages) : {}
      @session_status = existing_conversation ? @store.status(@selected_session.path) : nil
    end

    def current_leaf_id_for(existing_conversation)
      @current_tree_leaf_known = existing_conversation && @rpc_clients.active?(@selected_session.path)
      active_session_tree_leaf(@selected_session.path) if @current_tree_leaf_known
    end

    def selected_existing_session?
      @selected_session && File.exist?(@selected_session.path)
    end

    def active_session_tree_leaf(session_path)
      client = @rpc_clients.begin_use(session_path)
      return unless client&.respond_to?(:tree_leaf)

      client.tree_leaf
    ensure
      @rpc_clients.end_use(session_path) if client
    end
  end
end

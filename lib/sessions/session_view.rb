require_relative "../pi_session_store"
require_relative "session_family"
require_relative "sidebar"

module Sessions
  class SessionView
    PENDING_SESSION_DISPLAY_NAME = "New session (pending first assistant response)"
    CONVERSATION_WINDOW_MIN_MESSAGES = 20
    CONVERSATION_WINDOW_MAX_MESSAGES = 150
    CONVERSATION_WINDOW_BYTE_BUDGET = 128 * 1024

    attr_reader :store,
      :groups,
      :all_sessions,
      :selected_session,
      :session_family,
      :sidebar,
      :current_tree_leaf_known,
      :latest_tree_leaf_id,
      :conversation_tree_leaf_id,
      :viewing_older_tree_leaf,
      :session_sync_mode,
      :session_sync_revision,
      :session_sync_error,
      :messages,
      :conversation_start_index,
      :conversation_has_older_messages,
      :conversation_older_message_count,
      :attachment_counts,
      :attachment_images,
      :session_status

    def self.build(**kwargs)
      new(**kwargs).build
    end

    def self.older_window(sessions_root:, session_path:, cursor:, current_leaf_id:, attachment_store:, load_all: false)
      return empty_older_window unless session_path_within_root?(session_path, sessions_root)

      store = PiSessionStore.new(root: sessions_root)
      all_messages = store.messages(session_path, current_leaf_id: current_leaf_id)
      cursor = [[cursor.to_i, 0].max, all_messages.length].min
      messages = load_all ? all_messages.first(cursor) : conversation_window_before(all_messages, cursor)
      next_cursor = cursor - messages.length
      {
        messages: messages,
        next_cursor: next_cursor,
        has_older_messages: next_cursor.positive?,
        older_message_count: next_cursor,
        attachment_counts: attachment_store.counts_for_messages(session_path, messages),
        attachment_images: attachment_store.images_for_messages(session_path, messages)
      }
    rescue SystemCallError
      empty_older_window
    end

    def self.empty_older_window
      {
        messages: [],
        next_cursor: 0,
        has_older_messages: false,
        older_message_count: 0,
        attachment_counts: {},
        attachment_images: {}
      }
    end

    def self.session_path_within_root?(session_path, sessions_root)
      root = File.realpath(sessions_root)
      path = File.realpath(session_path)
      path.start_with?("#{root}#{File::SEPARATOR}") && File.file?(path)
    rescue SystemCallError, TypeError
      false
    end

    def initialize(
      sessions_root:,
      params:,
      include_conversation:,
      read_state_store:,
      pinned_session_store: nil,
      attachment_store:,
      rpc_clients:,
      session_synchronizer: nil,
      mark_selected_read:,
      pending_sessions: [],
      session_filter: nil,
      now: Time.now
    )
      @sessions_root = sessions_root
      @params = params
      @include_conversation = include_conversation
      @read_state_store = read_state_store
      @pinned_session_store = pinned_session_store
      @attachment_store = attachment_store
      @rpc_clients = rpc_clients
      @session_synchronizer = session_synchronizer
      @mark_selected_read = mark_selected_read
      @pending_sessions = pending_sessions
      @session_filter = session_filter
      @now = now
    end

    def build
      @store = PiSessionStore.new(root: @sessions_root, hide_missing_cwds: true)
      @groups = @store.grouped_sessions
      merge_pending_sessions
      @groups = filtered_groups(@groups)
      @all_sessions = @groups.values.flatten
      @read_state_store.observe_sessions(@all_sessions)
      @selected_session = find_selected_session
      @session_family = Sessions::SessionFamily.new(@all_sessions)
      @read_state_store.mark_read(@selected_session) if @selected_session && @mark_selected_read
      @sidebar = Sessions::Sidebar.new(
        groups: @groups,
        selected_session: @selected_session,
        params: @params,
        read_state_store: @read_state_store,
        pinned_session_store: @pinned_session_store
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
        :@session_family => @session_family,
        :@sidebar => @sidebar,
        :@current_tree_leaf_known => @current_tree_leaf_known,
        :@latest_tree_leaf_id => @latest_tree_leaf_id,
        :@conversation_tree_leaf_id => @conversation_tree_leaf_id,
        :@viewing_older_tree_leaf => @viewing_older_tree_leaf,
        :@session_sync_mode => @session_sync_mode,
        :@session_sync_revision => @session_sync_revision,
        :@session_sync_error => @session_sync_error,
        :@messages => @messages,
        :@conversation_start_index => @conversation_start_index,
        :@conversation_has_older_messages => @conversation_has_older_messages,
        :@conversation_older_message_count => @conversation_older_message_count,
        :@attachment_counts => @attachment_counts,
        :@attachment_images => @attachment_images,
        :@session_status => @session_status
      }
    end

    private

    def filtered_groups(groups)
      return groups unless @session_filter

      groups.each_with_object({}) do |(cwd, sessions), filtered|
        visible_sessions = sessions.select { |session| @session_filter.call(session) }
        filtered[cwd] = visible_sessions unless visible_sessions.empty?
      end
    end

    def merge_pending_sessions
      known_paths = @groups.values.flatten.to_h { |session| [session.path, true] }

      @pending_sessions.each do |path, cwd, created_at|
        next if known_paths[path] || File.exist?(path)
        next unless path == selected_session_path || @rpc_clients.active?(path)

        activity_at = created_at || @now
        pending_session = PiSessionStore::Session.new(
          path: path,
          cwd: cwd,
          id: File.basename(path, ".jsonl"),
          display_name: PENDING_SESSION_DISPLAY_NAME,
          first_user_message: nil,
          message_count: 0,
          created_at: activity_at,
          modified_at: activity_at,
          conversation_activity_at: activity_at
        )
        @groups[cwd] ||= []
        @groups[cwd].unshift(pending_session)
      end
    end

    def selected_session_path
      @params["session"].to_s
    end

    def find_selected_session
      return if @params["no_session"].to_s == "1"

      requested_session = @all_sessions.find { |session| session.path == selected_session_path }
      return requested_session if requested_session

      excluded_path = @params["session_fallback_excluding"].to_s
      @all_sessions
        .reject { |session| session.path == excluded_path }
        .max_by { |session| session.conversation_activity_at || Time.at(0) }
    end

    def prepare_conversation
      @current_tree_leaf_known = false
      @latest_tree_leaf_id = nil
      @conversation_tree_leaf_id = nil
      @viewing_older_tree_leaf = false
      @session_sync_mode = :available
      @session_sync_revision = nil
      @session_sync_error = nil
      @messages = []
      @conversation_start_index = 0
      @conversation_older_message_count = 0
      @conversation_has_older_messages = false
      @attachment_counts = {}
      @attachment_images = {}
      @session_status = nil
      return unless @include_conversation && selected_existing_session?

      sync_state = synchronized_session_state
      @session_sync_mode = sync_state&.mode || :available
      @session_sync_revision = sync_state&.revision
      @session_sync_error = sync_state&.error
      if sync_state
        @current_tree_leaf_known = sync_state.mode == :managed
        @conversation_tree_leaf_id = if @current_tree_leaf_known
          sync_state.rpc_leaf_id
        elsif sync_state.blocked?
          sync_state.persisted_leaf_id
        end
      else
        @conversation_tree_leaf_id = current_leaf_id_for(true)
      end
      conversation = @store.conversation(@selected_session.path, current_leaf_id: @conversation_tree_leaf_id)
      @latest_tree_leaf_id = conversation.latest_leaf_id
      @viewing_older_tree_leaf = @current_tree_leaf_known && @conversation_tree_leaf_id != @latest_tree_leaf_id
      @messages = latest_conversation_window(conversation.messages)
      @conversation_start_index = conversation.messages.length - @messages.length
      @conversation_older_message_count = @conversation_start_index
      @conversation_has_older_messages = @conversation_older_message_count.positive?
      @attachment_counts = @attachment_store.counts_for_messages(@selected_session.path, @messages)
      @attachment_images = @attachment_store.images_for_messages(@selected_session.path, @messages)
      @session_status = conversation.status
    end

    def latest_conversation_window(messages)
      self.class.conversation_window_before(messages, messages.length)
    end

    def self.conversation_window_before(messages, cursor)
      selected = []
      bytes = 0
      has_user_message = false
      messages.first(cursor).reverse_each do |message|
        message_bytes = conversation_window_message_bytes(message)
        enough_context = has_user_message || selected.length >= CONVERSATION_WINDOW_MIN_MESSAGES
        break if selected.length >= CONVERSATION_WINDOW_MAX_MESSAGES
        break if enough_context && bytes + message_bytes > CONVERSATION_WINDOW_BYTE_BUDGET

        selected << message
        bytes += message_bytes
        has_user_message ||= message.role == "user"
      end
      selected.reverse
    end

    def self.conversation_window_message_bytes(message)
      text_bytes = [message.role, message.text, message.summary].compact.sum { |value| value.to_s.bytesize }
      image_bytes = Array(message.images).sum { |image| (image[:data] || image["data"]).to_s.bytesize }
      (text_bytes * 2) + image_bytes
    end

    def synchronized_session_state
      return unless @session_synchronizer

      @session_synchronizer.inspect(@selected_session.path, include_position: true)
    end

    def current_leaf_id_for(existing_conversation)
      @current_tree_leaf_known = existing_conversation && @rpc_clients.active?(@selected_session.path)
      active_session_tree_leaf(@selected_session.path) if @current_tree_leaf_known
    rescue Errno::EPIPE, IOError
      @current_tree_leaf_known = false
      nil
    end

    def selected_existing_session?
      @selected_session && File.exist?(@selected_session.path)
    end

    def active_session_tree_leaf(session_path)
      self.class.active_session_tree_leaf(@rpc_clients, session_path)
    end

    def self.active_session_tree_leaf(rpc_clients, session_path)
      rpc_clients.with_active_client(session_path) do |client|
        client.tree_leaf if client.respond_to?(:tree_leaf)
      end
    end
  end
end

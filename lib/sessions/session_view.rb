require "digest"
require_relative "../pi_session_store"
require_relative "session_family"
require_relative "sidebar"

module Sessions
  class SessionView
    PENDING_SESSION_DISPLAY_NAME = "New session (pending first assistant response)"
    CONVERSATION_WINDOW_MIN_MESSAGES = 50
    CONVERSATION_WINDOW_MAX_MESSAGES = 150
    CONVERSATION_WINDOW_BYTE_BUDGET = 256 * 1024
    CONVERSATION_WINDOW_RAW_DETAILS_BYTE_CAP = 8 * 1024

    attr_reader :store,
      :groups,
      :all_sessions,
      :selected_session,
      :session_family,
      :sidebar,
      :current_tree_leaf_known,
      :latest_tree_leaf_id,
      :viewing_older_tree_leaf,
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

    def self.older_window(sessions_root:, session_path:, cursor:, attachment_store:, rpc_clients:)
      return empty_older_window unless session_path_within_root?(session_path, sessions_root)

      store = PiSessionStore.new(root: sessions_root, delete_missing_cwds: true)
      current_leaf_id = active_session_tree_leaf(rpc_clients, session_path) if rpc_clients.active?(session_path)
      all_messages = store.messages(session_path, current_leaf_id: current_leaf_id)
      cursor = [[cursor.to_i, 0].max, all_messages.length].min
      messages = conversation_window_before(all_messages, cursor)
      next_cursor = cursor - messages.length
      {
        messages: messages,
        start_index: next_cursor,
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
        start_index: 0,
        next_cursor: 0,
        has_older_messages: false,
        older_message_count: 0,
        attachment_counts: {},
        attachment_images: {}
      }
    end

    def self.raw_details(sessions_root:, session_path:, message_index:, raw_details_token:, rpc_clients:)
      return unless session_path_within_root?(session_path, sessions_root)

      store = PiSessionStore.new(root: sessions_root, delete_missing_cwds: true)
      current_leaf_id = active_session_tree_leaf(rpc_clients, session_path) if rpc_clients.active?(session_path)
      messages = store.messages(session_path, current_leaf_id: current_leaf_id)
      return if message_index.negative? || message_index >= messages.length

      message = messages[message_index]
      return unless raw_details_token.to_s == raw_details_token_for(message)

      message.raw_details.to_s
    rescue SystemCallError
      nil
    end

    def self.raw_details_token_for(message)
      Digest::SHA256.hexdigest([
        message.role,
        message.timestamp&.to_i,
        message.text,
        Digest::SHA256.hexdigest(message.raw_details.to_s)
      ].join("\0"))
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
      @session_family = Sessions::SessionFamily.new(@all_sessions)
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
        :@session_family => @session_family,
        :@sidebar => @sidebar,
        :@current_tree_leaf_known => @current_tree_leaf_known,
        :@latest_tree_leaf_id => @latest_tree_leaf_id,
        :@viewing_older_tree_leaf => @viewing_older_tree_leaf,
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
      all_messages = existing_session ? @store.messages(@selected_session.path, current_leaf_id: current_leaf_id) : []
      @messages = existing_conversation ? latest_conversation_window(all_messages) : all_messages
      @conversation_start_index = all_messages.length - @messages.length
      @conversation_older_message_count = @conversation_start_index
      @conversation_has_older_messages = @conversation_older_message_count.positive?
      @attachment_counts = existing_conversation ? @attachment_store.counts_for_messages(@selected_session.path, @messages) : {}
      @attachment_images = existing_conversation ? @attachment_store.images_for_messages(@selected_session.path, @messages) : {}
      @session_status = existing_conversation ? @store.status(@selected_session.path) : nil
    end

    def latest_conversation_window(messages)
      self.class.conversation_window_before(messages, messages.length)
    end

    def self.conversation_window_before(messages, cursor)
      return messages.first(cursor) if cursor <= CONVERSATION_WINDOW_MIN_MESSAGES

      selected = []
      bytes = 0
      messages.first(cursor).reverse_each do |message|
        message_bytes = conversation_window_message_bytes(message)
        break if selected.length >= CONVERSATION_WINDOW_MAX_MESSAGES
        break if selected.length >= CONVERSATION_WINDOW_MIN_MESSAGES && bytes + message_bytes > CONVERSATION_WINDOW_BYTE_BUDGET

        selected << message
        bytes += message_bytes
      end
      selected.reverse
    end

    def self.conversation_window_message_bytes(message)
      [message.role, message.text, message.summary].compact.sum { |value| value.to_s.bytesize } + [message.raw_details.to_s.bytesize, CONVERSATION_WINDOW_RAW_DETAILS_BYTE_CAP].min
    end

    def current_leaf_id_for(existing_conversation)
      @current_tree_leaf_known = existing_conversation && @rpc_clients.active?(@selected_session.path)
      active_session_tree_leaf(@selected_session.path) if @current_tree_leaf_known
    end

    def selected_existing_session?
      @selected_session && File.exist?(@selected_session.path)
    end

    def active_session_tree_leaf(session_path)
      self.class.active_session_tree_leaf(@rpc_clients, session_path)
    end

    def self.active_session_tree_leaf(rpc_clients, session_path)
      client = rpc_clients.begin_use(session_path)
      return unless client&.respond_to?(:tree_leaf)

      client.tree_leaf
    ensure
      rpc_clients.end_use(session_path) if client
    end
  end
end

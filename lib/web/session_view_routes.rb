require "json"
require_relative "../sessions/session_view"

module Web
  module SessionViewRoutes
    module Helpers
      private

      def prepare_session_view(include_conversation: false, defer_busy_metadata: false)
        remap_selected_pending_session
        pending_sessions = pending_session_registry.entries_with_created_at
        Sessions::SessionView.build(
          sessions_root: settings.sessions_root,
          params: params,
          include_conversation: include_conversation,
          read_state_store: read_state_store,
          pinned_session_store: pinned_session_store,
          attachment_store: attachment_store,
          rpc_clients: rpc_clients,
          session_synchronizer: session_synchronizer,
          mark_selected_read: should_mark_selected_session_read?,
          pending_sessions: pending_sessions,
          session_filter: workspace_session_filter,
          defer_busy_metadata: defer_busy_metadata
        ).to_instance_variables.each do |name, value|
          instance_variable_set(name, value)
        end
      end

      def should_mark_selected_session_read?
        request.path_info != "/sidebar" || !params["session"].to_s.empty?
      end

      def remap_selected_pending_session
        selected_path = params["session"]
        return if selected_path.to_s.empty?

        real_path = remap_active_pending_rpc_client(selected_path)
        params["session"] = real_path if real_path
      end

      def session_view_url
        query = {}
        query["session"] = @selected_session.path if @selected_session
        query["no_session"] = "1" if !@selected_session && @all_sessions.any?
        query["project"] = selected_project_cwd if selected_project_cwd
        query["session_search"] = sidebar_session_search_query if sidebar_session_search?
        if @selected_session && (sessions_limit = @sidebar.retained_sessions_limit_for(@selected_session.path))
          query["sidebar_sessions_limit"] = sessions_limit
        end
        query["session_only"] = "1" if params["session_only"].to_s == "1"
        "/?#{Rack::Utils.build_nested_query(query)}"
      end

      def session_redirect_path(session_path)
        query = { "session" => session_path }
        query["project"] = selected_project_cwd if selected_project_cwd
        query["session_search"] = sidebar_session_search_query if sidebar_session_search?
        query["session_only"] = "1" if params["session_only"].to_s == "1"
        "/?#{Rack::Utils.build_nested_query(query)}"
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.get "/" do
        prepare_session_view(include_conversation: true)
        erb :index
      end

      app.get "/sidebar" do
        prepare_session_view(defer_busy_metadata: true)
        erb :_sidebar, layout: false
      end

      app.get "/new_session_modal" do
        prepare_session_view
        erb :_new_session_modal, layout: false
      end

      app.get "/session_fragment" do
        prepare_session_view(include_conversation: true)
        content_type :json
        JSON.generate(
          url: session_view_url,
          title: @selected_session&.display_name.to_s,
          session: @selected_session&.path,
          sidebar_html: session_only? ? nil : erb(:_sidebar, layout: false),
          conversation_html: erb(:_conversation, layout: false),
          new_session_modal_html: erb(:_new_session_modal, layout: false),
          fork_session_modal_html: erb(:_fork_session_modal, layout: false)
        )
      end

      app.get "/conversation_older" do
        session_path = require_current_workspace_session!(params["session"].to_s)
        result = Sessions::SessionView.older_window(
          sessions_root: settings.sessions_root,
          session_path: session_path,
          cursor: params["cursor"].to_i,
          current_leaf_id: params["tree_leaf"].to_s.empty? ? nil : params["tree_leaf"].to_s,
          attachment_store: attachment_store,
          after_cursor: params.key?("after") ? params["after"].to_i : nil
        )
        content_type :json
        JSON.generate(
          html: result.fetch(:messages).map { |message|
            erb(:_message_article, layout: false, locals: { message: message, attachment_count: result.fetch(:attachment_counts)[message.object_id], attachment_images: result.fetch(:attachment_images)[message.object_id] })
          }.join,
          next_cursor: result.fetch(:next_cursor),
          has_older_messages: result.fetch(:has_older_messages),
          older_message_count: result.fetch(:older_message_count)
        )
      end

      app.get "/attachments/:session_hash/:file" do
        halt 404 unless params["session_hash"].to_s.match?(/\A[a-f0-9]{64}\z/)
        halt 404 unless params["file"].to_s.match?(/\A[a-f0-9]{64}\.(png|jpg|gif|webp)\z/)
        require_current_workspace_session_hash!(params["session_hash"].to_s)

        path = File.join(settings.attachments_root, params["session_hash"], params["file"])
        root = File.realpath(settings.attachments_root)
        real_path = File.realpath(path)
        halt 404 unless real_path.start_with?("#{root}#{File::SEPARATOR}")
        mime_type Rack::Mime.mime_type(File.extname(real_path))
        send_file real_path
      rescue SystemCallError
        halt 404
      end
    end
  end
end

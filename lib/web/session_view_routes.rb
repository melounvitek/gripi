require "json"
require_relative "../sessions/session_view"

module Web
  module SessionViewRoutes
    module Helpers
      private

      def prepare_session_view(include_conversation: false)
        remap_selected_pending_session
        Sessions::SessionView.build(
          sessions_root: settings.sessions_root,
          params: params,
          include_conversation: include_conversation,
          read_state_store: read_state_store,
          attachment_store: attachment_store,
          rpc_clients: rpc_clients,
          mark_selected_read: should_mark_selected_session_read?,
          pending_session_cwd: ->(path) { pending_rpc_cwd(path) }
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
        query["project"] = selected_project_cwd if selected_project_cwd
        query["session_search"] = sidebar_session_search_query if sidebar_session_search?
        "/?#{Rack::Utils.build_nested_query(query)}"
      end

      def session_redirect_path(session_path)
        query = { "session" => session_path }
        query["project"] = selected_project_cwd if selected_project_cwd
        query["session_search"] = sidebar_session_search_query if sidebar_session_search?
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
        prepare_session_view
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
          sidebar_html: erb(:_sidebar, layout: false),
          conversation_html: erb(:_conversation, layout: false),
          new_session_modal_html: erb(:_new_session_modal, layout: false),
          fork_session_modal_html: erb(:_fork_session_modal, layout: false)
        )
      end
    end
  end
end

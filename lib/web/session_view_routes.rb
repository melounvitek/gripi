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

      app.get "/conversation_older" do
        result = Sessions::SessionView.older_window(
          sessions_root: settings.sessions_root,
          session_path: params["session"].to_s,
          cursor: params["cursor"].to_i,
          attachment_store: attachment_store,
          rpc_clients: rpc_clients
        )
        content_type :json
        JSON.generate(
          html: result.fetch(:messages).map.with_index(result.fetch(:start_index)) { |message, message_index|
            erb(:_message_article, layout: false, locals: { message: message, message_index: message_index, attachment_count: result.fetch(:attachment_counts)[message.object_id], attachment_images: result.fetch(:attachment_images)[message.object_id] })
          }.join,
          start_index: result.fetch(:start_index),
          next_cursor: result.fetch(:next_cursor),
          has_older_messages: result.fetch(:has_older_messages),
          older_message_count: result.fetch(:older_message_count)
        )
      end

      app.get "/attachments/:session_hash/:file" do
        halt 404 unless params["session_hash"].to_s.match?(/\A[a-f0-9]{64}\z/)
        halt 404 unless params["file"].to_s.match?(/\A[a-f0-9]{64}\.(png|jpg|gif|webp)\z/)

        path = File.join(settings.attachments_root, params["session_hash"], params["file"])
        root = File.realpath(settings.attachments_root)
        real_path = File.realpath(path)
        halt 404 unless real_path.start_with?("#{root}#{File::SEPARATOR}")
        mime_type Rack::Mime.mime_type(File.extname(real_path))
        send_file real_path
      rescue SystemCallError
        halt 404
      end

      app.get "/message_raw_details" do
        message_index = Integer(params["message_index"], exception: false)
        halt 404 unless message_index

        raw_details = Sessions::SessionView.raw_details(
          sessions_root: settings.sessions_root,
          session_path: params["session"].to_s,
          message_index: message_index,
          raw_details_token: params["raw_details_token"],
          rpc_clients: rpc_clients
        )
        halt 404 if raw_details.nil? || raw_details.empty?

        content_type :json
        JSON.generate(raw_details: raw_details)
      end
    end
  end
end

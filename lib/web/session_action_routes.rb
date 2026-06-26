require "json"
require_relative "../prompts/slash_command"
require_relative "../prompts/uploaded_images"
require_relative "../rpc/branch_session"
require_relative "../rpc/command_catalog"
require_relative "../rpc/start_new_session"
require_relative "../pi_session_store"

module Web
  module SessionActionRoutes
    module Helpers
      private

      def json_request?
        request.env["HTTP_ACCEPT"].to_s.include?("application/json")
      end

      def redirect_to_new_session(new_session_path, command: nil)
        redirect_path = session_redirect_path(new_session_path)
        if json_request?
          content_type :json
          payload = { session: new_session_path, redirect: redirect_path }
          payload[:command] = command if command
          JSON.generate(payload)
        else
          redirect redirect_path
        end
      end

      def current_session_cwd(session_path)
        current_session = PiSessionStore.new(root: settings.sessions_root).sessions.find { |session| session.path == session_path }
        current_session&.cwd || pending_rpc_cwd(session_path) || File.dirname(session_path)
      end

      def start_new_session(cwd)
        Rpc::StartNewSession.call(
          cwd,
          client_factory: settings.new_rpc_client_factory.first,
          rpc_clients: rpc_clients,
          pending_sessions: pending_session_registry,
          sessions_root: settings.sessions_root
        )
      end

      def redirect_to_rpc_session_after_branch(previous_session_path, response, text: nil)
        data = response_data(response)
        if data.is_a?(Hash) && data["cancelled"]
          status 409 if json_request?
          content_type :json if json_request?
          return JSON.generate(cancelled: true, session: previous_session_path) if json_request?

          redirect session_redirect_path(previous_session_path)
        end

        new_session_path = branch_session_path(previous_session_path)
        redirect_path = session_redirect_path(new_session_path)
        if json_request?
          content_type :json
          payload = { session: new_session_path, redirect: redirect_path }
          payload[:text] = text if text
          JSON.generate(payload)
        else
          redirect redirect_path
        end
      end

      def branch_session_path(previous_session_path)
        Rpc::BranchSession.call(
          previous_session_path,
          rpc_clients: rpc_clients,
          pending_sessions: pending_session_registry,
          cwd: branched_session_cwd(previous_session_path)
        )
      end

      def branched_session_cwd(previous_session_path)
        session_cwd(previous_session_path) || pending_rpc_cwd(previous_session_path) || File.dirname(previous_session_path)
      end

      def validated_session_cwd(raw_cwd)
        cwd = raw_cwd.to_s.strip
        return { valid: false, error: "Enter an existing directory." } if cwd.empty?

        expanded_cwd = File.expand_path(cwd)
        return { valid: false, error: "Path must be an existing directory." } unless File.directory?(expanded_cwd)
        return { valid: false, error: "Directory is not accessible." } unless File.readable?(expanded_cwd) && File.executable?(expanded_cwd)

        { valid: true, cwd: File.realpath(expanded_cwd) }
      rescue ArgumentError, Errno::ENOENT, Errno::EACCES
        { valid: false, error: "Path must be an existing directory." }
      end

      def prompt_images_from(upload_param)
        Prompts::UploadedImages.parse(upload_param)
      rescue Prompts::UploadedImages::ValidationError => error
        halt 400, error.message
      end

      def message_with_attachment_paths(message, paths)
        paths.empty? ? message : [message.strip, paths.join("\n")].reject(&:empty?).join("\n\n")
      end

      def command_session_available?(session_path)
        rpc_clients.active?(session_path) || known_session_path?(session_path)
      end

      def known_session_path?(session_path)
        PiSessionStore.new(root: settings.sessions_root).sessions.any? { |session| session.path == session_path }
      end

      def commands_for(session_path)
        response = with_rpc_client(session_path) { |client| client.get_commands }
        Rpc::CommandCatalog.commands_from(response)
      rescue Errno::EPIPE, IOError
        Rpc::CommandCatalog.builtin_commands
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.post "/prompt" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        message = params.fetch("message").to_s
        images = prompt_images_from(params["images"])
        attachment_paths = attachment_store.persist_prompt_images(session_path, images)
        rpc_message = message_with_attachment_paths(message, attachment_paths)
        halt 400, "Message cannot be empty" if rpc_message.strip.empty? && images.empty?

        follow_up_prompt = params["streaming_behavior"].to_s == "follow_up"

        slash_command = follow_up_prompt ? nil : Prompts::SlashCommand.parse(message)
        branch_response = nil
        if follow_up_prompt
          submitted_at = Time.now
          with_rpc_client(session_path) { |client| client.follow_up(rpc_message, images) }
          attachment_store.record_prompt(session_path, rpc_message, images.length, timestamp: submitted_at, paths: attachment_paths, mime_types: images.map { |image| image[:mimeType] })
        elsif slash_command&.type == :rename && slash_command.name
          with_rpc_client(session_path) { |client| client.set_session_name(slash_command.name) }
        elsif slash_command&.type == :rename || [:fork, :tree].include?(slash_command&.type)
          nil
        elsif slash_command&.type == :compact
          with_rpc_client(session_path) { |client| client.compact(slash_command.instructions) }
        elsif slash_command&.type == :new
          branch_response = redirect_to_new_session(start_new_session(current_session_cwd(session_path)), command: "new")
        elsif slash_command&.type == :clone
          response = with_rpc_client(session_path) { |client| client.clone_session }
          branch_response = redirect_to_rpc_session_after_branch(session_path, response)
        else
          submitted_at = Time.now
          with_rpc_client(session_path) { |client| client.prompt(rpc_message, images) }
          attachment_store.record_prompt(session_path, rpc_message, images.length, timestamp: submitted_at, paths: attachment_paths, mime_types: images.map { |image| image[:mimeType] })
        end
        redirect_path = session_redirect_path(session_path)
        if branch_response
          branch_response
        elsif json_request?
          content_type :json
          payload = { session: session_path, redirect: redirect_path }
          payload[:follow_up] = true if follow_up_prompt && !slash_command
          if slash_command
            payload[:command] = slash_command.type.to_s
            payload[:name] = slash_command.name if slash_command.name
            payload[:error] = slash_command.error if slash_command.error
          end
          JSON.generate(payload)
        else
          redirect redirect_path
        end
      end

      app.get "/sessions/validate_cwd" do
        result = validated_session_cwd(params["cwd"])
        content_type :json
        if result.fetch(:valid)
          JSON.generate(valid: true, cwd: result.fetch(:cwd))
        else
          status 422
          JSON.generate(valid: false, error: result.fetch(:error))
        end
      end

      app.post "/sessions/new" do
        session_path = params.fetch("session")
        redirect_to_new_session(start_new_session(current_session_cwd(session_path)))
      end

      app.post "/sessions/new_at_cwd" do
        result = validated_session_cwd(params["cwd"])
        unless result.fetch(:valid)
          if json_request?
            status 422
            content_type :json
            next JSON.generate(valid: false, error: result.fetch(:error))
          end
          halt 422, result.fetch(:error)
        end

        redirect_to_new_session(start_new_session(result.fetch(:cwd)))
      end

      app.get "/sessions/fork_messages" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        response = with_rpc_client(session_path) { |client| client.get_fork_messages }
        messages = response_data(response).fetch("messages", [])
        content_type :json
        JSON.generate(messages: messages)
      end

      app.get "/sessions/tree_entries" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        current_leaf_id = with_rpc_client(session_path) { |client| client.tree_leaf }
        store = PiSessionStore.new(root: settings.sessions_root)
        entries = store.tree_entries(session_path, current_leaf_id: current_leaf_id)
        content_type :json
        JSON.generate(entries: entries)
      end

      app.post "/sessions/tree" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Tree entry cannot be empty" if entry_id.empty?

        response = with_rpc_client(session_path) { |client| client.navigate_tree(entry_id) }
        data = response_data(response)
        payload = { session: session_path, redirect: session_redirect_path(session_path), cancelled: data.is_a?(Hash) ? data["cancelled"] || false : false }
        content_type :json
        JSON.generate(payload)
      end

      app.post "/sessions/fork" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Fork entry cannot be empty" if entry_id.empty?

        response = with_rpc_client(session_path) { |client| client.fork(entry_id) }
        redirect_to_rpc_session_after_branch(session_path, response, text: response_data(response)["text"])
      end

      app.post "/sessions/clone" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        response = with_rpc_client(session_path) { |client| client.clone_session }
        redirect_to_rpc_session_after_branch(session_path, response)
      end

      app.post "/abort" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        with_rpc_client(session_path) { |client| client.abort }
        if json_request?
          content_type :json
          JSON.generate(ok: true, session: session_path)
        else
          redirect session_redirect_path(session_path)
        end
      end

      app.post "/compact" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        instructions = params["instructions"].to_s.strip
        with_rpc_client(session_path) { |client| client.compact(instructions.empty? ? nil : instructions) }
        redirect session_redirect_path(session_path)
      end

      app.post "/rename" do
        session_path = canonical_rpc_session_path(params.fetch("session"))
        name = params.fetch("name").to_s.strip
        halt 400, "Name cannot be empty" if name.empty?

        with_rpc_client(session_path) { |client| client.set_session_name(name) }
        redirect session_redirect_path(session_path)
      end

      app.get "/events" do
        session_path = params.fetch("session")
        after_seq = params.fetch("after", 0).to_i
        content_type :json
        JSON.generate(rpc_clients.events_after(session_path, after_seq))
      end

      app.get "/status" do
        session_path = params.fetch("session")
        halt 404 unless File.exist?(session_path)

        content_type :json
        status = PiSessionStore.new(root: settings.sessions_root).status(session_path)
        JSON.generate(
          context: format_context_usage(status),
          model: format_model(status),
          thinking: status.thinking_level
        )
      end

      app.get "/commands" do
        session_path = params.fetch("session")
        halt 404 unless command_session_available?(session_path)

        @commands = commands_for(session_path)
        erb :_commands, layout: false
      end

      app.post "/markdown" do
        content_type :json
        JSON.generate(html: markdown_renderer.render(params.fetch("text").to_s))
      end
    end
  end
end

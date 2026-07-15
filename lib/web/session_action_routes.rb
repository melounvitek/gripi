require "json"
require_relative "../prompts/slash_command"
require_relative "../prompts/uploaded_images"
require_relative "../rpc/branch_session"
require_relative "../rpc/command_catalog"
require_relative "../rpc/start_new_session"
require_relative "../pi_session_store"

module Web
  module SessionActionRoutes
    CWD_SUGGESTION_LIMIT = 30

    module Helpers
      private

      def json_request?
        request.env["HTTP_ACCEPT"].to_s.include?("application/json")
      end

      def redirect_to_new_session(new_session_path, command: nil)
        claim_session_for_current_workspace(new_session_path)
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

        new_session_path = claim_session_for_current_workspace(branch_session_path(previous_session_path))
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

      def normalized_session_cwd(raw_cwd)
        cwd = raw_cwd.to_s
        cwd.strip if cwd.valid_encoding?
      end

      def validated_session_cwd(raw_cwd)
        cwd = normalized_session_cwd(raw_cwd)
        return { valid: false, error: "Path must be an existing directory." } unless cwd
        return { valid: false, error: "Enter an existing directory." } if cwd.empty?

        expanded_cwd = File.expand_path(cwd)
        return { valid: false, error: "Path must be an existing directory." } unless File.directory?(expanded_cwd)
        return { valid: false, error: "Directory is not accessible." } unless File.readable?(expanded_cwd) && File.executable?(expanded_cwd)

        { valid: true, cwd: File.realpath(expanded_cwd) }
      rescue ArgumentError, Errno::ENOENT, Errno::EACCES
        { valid: false, error: "Path must be an existing directory." }
      end

      def browsed_session_cwd(raw_cwd)
        cwd = normalized_session_cwd(raw_cwd)
        validation = validated_session_cwd(cwd)
        return validation.merge(directories: []) if !cwd || cwd.empty?

        expanded_cwd = File.expand_path(cwd)
        parent, prefix = if File.directory?(expanded_cwd)
          [expanded_cwd, ""]
        else
          [File.dirname(expanded_cwd), File.basename(expanded_cwd)]
        end
        return validation.merge(directories: []) unless File.readable?(parent) && File.executable?(parent)

        directories = Dir.children(parent).sort.filter_map do |name|
          name = name.dup.force_encoding(Encoding::UTF_8)
          next unless name.valid_encoding?
          next if name.start_with?(".") && !prefix.start_with?(".")
          next unless name.start_with?(prefix)

          path = File.join(parent, name)
          path if File.directory?(path) && File.readable?(path) && File.executable?(path)
        rescue SystemCallError
          nil
        end.first(CWD_SUGGESTION_LIMIT)

        validation.merge(directories: directories)
      rescue ArgumentError, SystemCallError
        validation.merge(directories: [])
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
        response = with_synchronized_rpc_client(session_path) { |client| client.get_commands }
        Rpc::CommandCatalog.commands_from(response)
      rescue Errno::EPIPE, IOError
        Rpc::CommandCatalog.builtin_commands
      end

      def halt_failed_rpc_prompt(response)
        return unless response.is_a?(Hash) && response["success"] == false

        error = response["error"].to_s.strip
        error = "Prompt failed to send" if error.empty?
        status 422
        if json_request?
          content_type :json
          halt JSON.generate(success: false, error: error)
        end
        halt error
      end

      def halt_if_rpc_session_busy(session_path, client = nil)
        busy = client ? client.respond_to?(:busy?) && client.busy? : rpc_clients.busy?(session_path)
        return unless busy

        status 409
        content_type :json
        halt JSON.generate(error: "Session is busy")
      end

      def halt_failed_rpc_setting(response)
        return if response.is_a?(Hash) && response["success"] == true

        error = response.is_a?(Hash) ? response["error"].to_s.strip : ""
        error = "Setting could not be changed" if error.empty?
        status 422
        content_type :json
        halt JSON.generate(success: false, error: error)
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.post "/prompt" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        halt_if_session_sync_blocked(session_path)
        message = params.fetch("message").to_s
        images = prompt_images_from(params["images"])
        halt 400, "Message cannot be empty" if message.strip.empty? && images.empty?

        follow_up_prompt = params["streaming_behavior"].to_s == "follow_up"
        slash_command = follow_up_prompt ? nil : Prompts::SlashCommand.parse(message)
        branch_response = nil
        name_response = nil
        if follow_up_prompt
          submitted_at = Time.now
          attachment_paths = []
          rpc_message = message
          response = with_synchronized_rpc_client(session_path) do |client|
            attachment_paths = attachment_store.persist_prompt_images(session_path, images)
            rpc_message = message_with_attachment_paths(message, attachment_paths)
            client.follow_up(rpc_message, images)
          end
          halt_failed_rpc_prompt(response)
          attachment_store.record_prompt(session_path, rpc_message, images.length, timestamp: submitted_at, paths: attachment_paths, mime_types: images.map { |image| image[:mimeType] })
        elsif slash_command&.type == :name && slash_command.name
          response = with_synchronized_rpc_client(session_path) { |client| client.set_session_name(slash_command.name) }
          halt_failed_rpc_setting(response)
        elsif slash_command&.type == :name
          state_response = with_synchronized_rpc_client(session_path) { |client| client.get_state }
          halt_failed_rpc_setting(state_response)
          state = response_data(state_response)
          current_name = state.is_a?(Hash) ? state["sessionName"].to_s : ""
          name_response = current_name.empty? ? { error: "Usage: /name <name>" } : { name: current_name, current: true }
        elsif [:fork, :tree, :model].include?(slash_command&.type)
          nil
        elsif slash_command&.type == :compact
          with_synchronized_rpc_client(session_path) { |client| client.compact(slash_command.instructions) }
        elsif slash_command&.type == :new
          branch_response = redirect_to_new_session(start_new_session(current_session_cwd(session_path)), command: "new")
        elsif slash_command&.type == :clone
          response = with_synchronized_rpc_client(session_path) { |client| client.clone_session }
          branch_response = redirect_to_rpc_session_after_branch(session_path, response)
        else
          submitted_at = Time.now
          attachment_paths = []
          rpc_message = message
          response = with_synchronized_rpc_client(session_path) do |client|
            attachment_paths = attachment_store.persist_prompt_images(session_path, images)
            rpc_message = message_with_attachment_paths(message, attachment_paths)
            client.prompt(rpc_message, images)
          end
          halt_failed_rpc_prompt(response)
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
            payload.merge!(name_response) if name_response
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

      app.get "/sessions/browse_cwd" do
        headers "Cache-Control" => "no-store"
        content_type :json
        JSON.generate(browsed_session_cwd(params["cwd"]))
      end

      app.get "/sessions/model_settings" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        state_response = with_synchronized_rpc_client(session_path) { |client| client.get_state }
        models_response = with_synchronized_rpc_client(session_path) { |client| client.get_available_models }
        state = state_response["data"] if state_response.is_a?(Hash) && state_response["success"] == true
        models = models_response.dig("data", "models") if models_response.is_a?(Hash) && models_response["success"] == true
        unless state.is_a?(Hash) && models.is_a?(Array)
          status 502
          content_type :json
          halt JSON.generate(error: "Could not load model settings")
        end

        content_type :json
        JSON.generate(state: state, models: models)
      end

      app.post "/sessions/model_settings" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        provider = params.fetch("provider").to_s.strip
        model_id = params.fetch("model").to_s.strip
        thinking_level = params.fetch("thinking").to_s.strip
        halt 400, "Provider cannot be empty" if provider.empty?
        halt 400, "Model cannot be empty" if model_id.empty?
        halt 400, "Invalid thinking level" unless %w[off minimal low medium high xhigh max].include?(thinking_level)

        state_response = with_synchronized_rpc_client(session_path) do |client|
          halt_if_rpc_session_busy(session_path, client)
          halt_failed_rpc_setting(client.set_model(provider, model_id))
          halt_failed_rpc_setting(client.set_thinking_level(thinking_level))
          client.get_state
        end
        halt_failed_rpc_setting(state_response)
        state = state_response["data"]
        unless state.is_a?(Hash) && state["model"].is_a?(Hash) && state["thinkingLevel"].is_a?(String)
          status 502
          content_type :json
          halt JSON.generate(error: "Could not confirm model settings")
        end

        content_type :json
        JSON.generate(model: state["model"], thinking: state["thinkingLevel"])
      end

      app.post "/sessions/cycle_thinking" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        response = with_synchronized_rpc_client(session_path) do |client|
          halt_if_rpc_session_busy(session_path, client)
          client.cycle_thinking_level
        end
        halt_failed_rpc_setting(response)
        data = response["data"]
        unless data.nil? || (data.is_a?(Hash) && %w[off minimal low medium high xhigh max].include?(data["level"]))
          status 502
          content_type :json
          halt JSON.generate(error: "Could not change thinking level")
        end

        content_type :json
        JSON.generate(thinking: data&.fetch("level"))
      end

      app.post "/sessions/new" do
        session_path = require_current_workspace_session!(params.fetch("session"))
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

        params.delete("project")
        redirect_to_new_session(start_new_session(result.fetch(:cwd)))
      end

      app.get "/sessions/fork_messages" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        response = with_synchronized_rpc_client(session_path) { |client| client.get_fork_messages }
        messages = response_data(response).fetch("messages", [])
        content_type :json
        JSON.generate(messages: messages)
      end

      app.get "/sessions/tree_entries" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        current_leaf_id = with_synchronized_rpc_client(session_path) { |client| client.tree_leaf }
        store = PiSessionStore.new(root: settings.sessions_root)
        entries = store.tree_entries(session_path, current_leaf_id: current_leaf_id)
        content_type :json
        JSON.generate(entries: entries)
      end

      app.post "/sessions/tree" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Tree entry cannot be empty" if entry_id.empty?

        response = with_synchronized_rpc_client(session_path) { |client| client.navigate_tree(entry_id) }
        halt_failed_rpc_setting(response)
        data = response_data(response)
        payload = { session: session_path, redirect: session_redirect_path(session_path), cancelled: data.is_a?(Hash) ? data["cancelled"] || false : false }
        content_type :json
        JSON.generate(payload)
      end

      app.post "/sessions/fork" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Fork entry cannot be empty" if entry_id.empty?

        response = with_synchronized_rpc_client(session_path) { |client| client.fork(entry_id) }
        redirect_to_rpc_session_after_branch(session_path, response, text: response_data(response)["text"])
      end

      app.post "/sessions/clone" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        response = with_synchronized_rpc_client(session_path) { |client| client.clone_session }
        redirect_to_rpc_session_after_branch(session_path, response)
      end

      app.post "/abort" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        with_synchronized_interrupt_rpc_client(session_path) { |client| client.abort }
        if json_request?
          content_type :json
          JSON.generate(ok: true, session: session_path)
        else
          redirect session_redirect_path(session_path)
        end
      end

      app.post "/compact" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        instructions = params["instructions"].to_s.strip
        with_synchronized_rpc_client(session_path) { |client| client.compact(instructions.empty? ? nil : instructions) }
        redirect session_redirect_path(session_path)
      end

      app.get "/events" do
        session_path = require_current_workspace_session!(params.fetch("session"))
        after_seq = params.fetch("after", 0).to_i
        payload = rpc_clients.events_after(session_path, after_seq)
        sync_state = File.exist?(session_path) ? session_sync_state(session_path) : nil
        payload[:session_sync] = {
          mode: sync_state&.mode || :available,
          revision: sync_state&.revision,
          error: sync_state&.error,
          gateway_busy: rpc_clients.busy?(session_path)
        }
        cleanup_idle_rpc_clients
        content_type :json
        JSON.generate(payload)
      end

      app.post "/sessions/takeover" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        begin
          sync_state = session_synchronizer.take_over(session_path)
        rescue Sessions::SessionSynchronizer::BusyError => error
          status 409
          content_type :json
          next JSON.generate(error: error.message)
        rescue Sessions::SessionSynchronizer::BlockedError => error
          halt_session_sync_error(error)
        end

        content_type :json
        JSON.generate(ok: true, session: session_path, session_sync: { mode: sync_state.mode, revision: sync_state.revision })
      end

      app.post "/sessions/pin" do
        session_path = require_current_workspace_session!(params.fetch("session"))
        halt 404 unless known_session_path?(session_path)
        pinned = case params.fetch("pinned")
        when "true" then true
        when "false" then false
        else halt 400, "Invalid pinned state"
        end

        pinned ? pinned_session_store.pin(session_path) : pinned_session_store.unpin(session_path)
        content_type :json
        JSON.generate(session: session_path, pinned: pinned)
      end

      app.post "/sessions/mark_read" do
        session_path = require_current_workspace_session!(params.fetch("session"))
        session = PiSessionStore.new(root: settings.sessions_root).sessions.find { |candidate| candidate.path == session_path }
        halt 404 unless session

        read_state_store.mark_read(session)
        status 204
      end

      app.get "/status" do
        session_path = require_current_workspace_session!(params.fetch("session"))
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
        session_path = require_current_workspace_session!(params.fetch("session"))
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

require "json"
require_relative "../prompts/bash_command"
require_relative "../prompts/slash_command"
require_relative "../prompts/uploaded_images"
require_relative "../rpc/branch_session"
require_relative "../rpc/command_catalog"
require_relative "../rpc/start_new_session"
require_relative "../rpc/stop_session"
require_relative "../rpc/tree_projection"
require_relative "../pi_session_store"

module Web
  module SessionActionRoutes
    CWD_SUGGESTION_LIMIT = 30
    TREE_ENTRY_ID_BYTES = 1_024
    TREE_LABEL_BYTES = 4_096
    TREE_CUSTOM_INSTRUCTIONS_BYTES = 64 * 1_024

    module Helpers
      private

      def json_request?
        request.env["HTTP_ACCEPT"].to_s.include?("application/json")
      end

      def halt_if_tree_value_too_long(value, byte_limit, message)
        halt 400, message if value.to_s.bytesize > byte_limit
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
      rescue PiRpcClient::RequestTimeout => error
        status 504
        content_type :json
        halt JSON.generate(error: error.message)
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
        !PiSessionStore.new(root: settings.sessions_root).cwd_for_session(session_path).nil?
      end

      def commands_for(session_path)
        response = with_synchronized_rpc_client(session_path) { |client| client.get_commands }
        Rpc::CommandCatalog.commands_from(response)
      rescue PiRpcClientRegistry::OperationPending, PiRpcClientRegistry::ClientRetiring, PiRpcClientRegistry::ClientStarting, Errno::EPIPE, IOError
        Rpc::CommandCatalog.builtin_commands
      end

      def live_session_status(session_path)
        responses = rpc_clients.with_observing_client(session_path, touch: false) do |client|
          [client.get_state, client.get_session_stats]
        end
        return unless responses

        state_response, stats_response = responses
        state = state_response["data"] if state_response.is_a?(Hash) && state_response["success"] == true
        stats = stats_response["data"] if stats_response.is_a?(Hash) && stats_response["success"] == true
        context = stats["contextUsage"] if stats.is_a?(Hash)
        model = state["model"] if state.is_a?(Hash)
        if model.is_a?(Hash)
          provider = model["provider"] if model["provider"].is_a?(String) && !model["provider"].empty?
          model_id = model["id"] if model["id"].is_a?(String) && !model["id"].empty?
        end
        if state.is_a?(Hash)
          thinking_level = state["thinkingLevel"] if state["thinkingLevel"].is_a?(String) && !state["thinkingLevel"].empty?
        end
        context_values = [context["tokens"], context["contextWindow"], context["percent"]] if context.is_a?(Hash)
        context = nil unless context_values&.all? { |value| value.nil? || value.is_a?(Numeric) }
        {
          provider: provider,
          model_id: model_id,
          thinking_level: thinking_level,
          context: context,
          disk_independent: !!(provider && model_id && thinking_level && context)
        }
      rescue PiRpcClientRegistry::OperationPending, PiRpcClientRegistry::ClientRetiring, PiRpcClientRegistry::ClientStarting, Errno::EPIPE, IOError
        nil
      end

      def apply_live_session_status(status, live_status)
        return status unless live_status

        status.provider = live_status[:provider] if live_status[:provider]
        status.model_id = live_status[:model_id] if live_status[:model_id]
        status.thinking_level = live_status[:thinking_level] if live_status[:thinking_level]

        context = live_status[:context]
        if context.is_a?(Hash)
          status.context_tokens = context["tokens"]
          status.context_limit = context["contextWindow"]
          status.context_percent = context["percent"]
          status.context_estimated = false
        end
        status
      end

      def abort_rpc_client(client)
        if client.respond_to?(:active_bash_command) && client.active_bash_command && client.respond_to?(:abort_bash)
          client.abort_bash
        else
          client.abort
        end
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
        message = params.fetch("message").to_s
        images = prompt_images_from(params["images"])
        halt 400, "Message cannot be empty" if message.strip.empty? && images.empty?

        bash_command = Prompts::BashCommand.parse(message) unless params["bash_mode"] == "prompt"
        if bash_command
          unless images.empty?
            status 400
            if json_request?
              content_type :json
              halt JSON.generate(error: "Images cannot be attached to bash commands")
            end
            halt "Images cannot be attached to bash commands"
          end

          response = with_synchronized_bash_rpc_client(session_path) do |client|
            client.bash(bash_command.command, exclude_from_context: bash_command.exclude_from_context)
          end
          halt_failed_rpc_prompt(response) unless json_request?
          redirect_path = session_redirect_path(session_path)
          if json_request?
            content_type :json
            payload = {
              command: "bash",
              bash_id: response["id"],
              data: response_data(response),
              exclude_from_context: bash_command.exclude_from_context,
              session: session_path,
              redirect: redirect_path
            }
            payload[:error] = response["error"] if response["success"] == false
            next JSON.generate(payload)
          end
          redirect redirect_path
        end

        streaming_behavior = params["streaming_behavior"].to_s
        halt 400, "Invalid streaming behavior" unless ["", "steer", "follow_up"].include?(streaming_behavior)
        queued_prompt = !streaming_behavior.empty?
        follow_up_prompt = streaming_behavior == "follow_up"
        compacting = rpc_clients.compacting?(session_path)
        if streaming_behavior == "steer" && compacting
          status 409
          content_type :json
          halt JSON.generate(error: "Steering is unavailable during compaction")
        end
        if follow_up_prompt && compacting
          halt_if_known_session_sync_blocked(session_path)
        else
          halt_if_session_sync_blocked(session_path)
        end
        slash_command = queued_prompt ? nil : Prompts::SlashCommand.parse(message)
        branch_response = nil
        name_response = nil
        prompt_running = nil
        if queued_prompt
          submitted_at = Time.now
          attachment_paths = []
          rpc_message = message
          prepare_queued_payload = lambda do
            next unless attachment_paths.empty? && rpc_message == message

            attachment_paths = attachment_store.persist_prompt_images(session_path, images)
            rpc_message = message_with_attachment_paths(message, attachment_paths)
          end
          submit_queued_prompt = lambda do |client|
            prepare_queued_payload.call
            if follow_up_prompt
              client.follow_up(rpc_message, images)
            else
              if client.respond_to?(:compacting?) && client.compacting?
                status 409
                content_type :json
                halt JSON.generate(error: "Steering is unavailable during compaction")
              end
              client.steer(rpc_message, images)
            end
          end
          if follow_up_prompt
            response = with_compacting_rpc_client(session_path) do |client|
              prepare_queued_payload.call
              client.queue_follow_up_during_compaction(rpc_message, images) if client.respond_to?(:queue_follow_up_during_compaction)
            end
          end
          response ||= with_synchronized_rpc_client(session_path, &submit_queued_prompt)
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
            prompt_response = client.prompt(rpc_message, images)
            if message.strip.start_with?("/") && !message.match?(/[\r\n]/)
              state = response_data(client.get_state)
              prompt_running = state["isStreaming"] if state.is_a?(Hash) && [true, false].include?(state["isStreaming"])
            end
            prompt_response
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
          payload[:steer] = true if streaming_behavior == "steer"
          payload[:follow_up] = true if follow_up_prompt
          payload[:queued_after_compaction] = true if follow_up_prompt && response.is_a?(Hash) && response["compacting"] == true
          payload[:running] = prompt_running unless prompt_running.nil?
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
        filter_mode = params["filter"].to_s
        halt 400, "Invalid tree filter" if !filter_mode.empty? && !Rpc::TreeProjection::FILTER_MODES.include?(filter_mode)

        snapshot_response = with_synchronized_rpc_client(session_path) do |client|
          client.tree_snapshot(filter_mode.empty? ? nil : filter_mode)
        end
        halt_failed_rpc_setting(snapshot_response)
        snapshot = response_data(snapshot_response)
        unless snapshot.is_a?(Hash) && snapshot["entries"].is_a?(Array) && snapshot["settings"].is_a?(Hash) && Rpc::TreeProjection::FILTER_MODES.include?(snapshot["filter"])
          status 502
          content_type :json
          halt JSON.generate(error: "Could not load session tree")
        end

        content_type :json
        JSON.generate(snapshot)
      end

      app.post "/sessions/tree" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Tree entry cannot be empty" if entry_id.empty?
        halt_if_tree_value_too_long(entry_id, TREE_ENTRY_ID_BYTES, "Tree entry id is too long")
        summary = (params["summary_mode"] || params["summary"] || "none").to_s
        halt 400, "Invalid summary mode" unless %w[none default custom].include?(summary)
        custom_instructions = (params["custom_instructions"] || params["instructions"]).to_s.strip
        halt 400, "Custom summary instructions cannot be empty" if summary == "custom" && custom_instructions.empty?
        halt_if_tree_value_too_long(custom_instructions, TREE_CUSTOM_INSTRUCTIONS_BYTES, "Custom summary instructions are too long")

        response = with_synchronized_rpc_client(session_path) do |client|
          halt_if_rpc_session_busy(session_path, client)
          client.navigate_tree(entry_id, summary: summary, custom_instructions: summary == "custom" ? custom_instructions : nil)
        end
        halt_failed_rpc_setting(response)
        data = response_data(response)
        payload = { session: session_path, redirect: session_redirect_path(session_path), cancelled: data.is_a?(Hash) ? data["cancelled"] || false : false }
        payload[:editorText] = data["editorText"] if data.is_a?(Hash) && data["editorText"].is_a?(String)
        content_type :json
        JSON.generate(payload)
      end

      app.post "/sessions/tree/label" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        entry_id = params.fetch("entry_id").to_s
        halt 400, "Tree entry cannot be empty" if entry_id.empty?
        halt_if_tree_value_too_long(entry_id, TREE_ENTRY_ID_BYTES, "Tree entry id is too long")
        label = params["label"].to_s.strip
        halt_if_tree_value_too_long(label, TREE_LABEL_BYTES, "Label is too long")

        response = with_synchronized_rpc_client(session_path) do |client|
          halt_if_rpc_session_busy(session_path, client)
          client.set_tree_label(entry_id, label.empty? ? nil : label)
        end
        halt_failed_rpc_setting(response)
        content_type :json
        JSON.generate(entryId: entry_id, label: label.empty? ? nil : label)
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

      app.post "/extension_ui_response" do
        session_path = require_current_workspace_session!(canonical_rpc_session_path(params.fetch("session")))
        id = params.fetch("id", "").to_s
        halt 400, "Missing extension UI request id" if id.empty? || id.bytesize > 1_024

        cancelled = params["cancelled"].to_s == "true"
        confirmed = params.key?("confirmed") ? params["confirmed"].to_s == "true" : nil
        value = params.key?("value") ? params["value"].to_s : nil
        halt 400, "Invalid extension UI response" unless cancelled || !confirmed.nil? || !value.nil?

        delivered = with_existing_control_rpc_client(session_path) do |client|
          client.extension_ui_response(id, value: value, confirmed: confirmed, cancelled: cancelled)
        end
        halt 404, "No active Pi session" unless delivered
        halt_failed_rpc_setting(delivered)
        content_type :json
        JSON.generate(ok: true, session: session_path)
      end

      app.post "/abort" do
        requested_path = require_current_workspace_session!(params.fetch("session"))
        session_path = requested_path
        result = Rpc::StopSession.call do
          pending_path = stop_matching_pending_rpc_session(requested_path) { |client| abort_rpc_client(client) } unless rpc_clients.active?(requested_path)
          if pending_path
            session_path = pending_path
          else
            with_synchronized_interrupt_rpc_client(session_path) { |client| abort_rpc_client(client) }
          end
        end
        if json_request?
          status 202 if result.stopping
          content_type :json
          payload = { ok: true, session: session_path }
          payload[:forced] = true if result.forced
          payload[:stopping] = true if result.stopping
          JSON.generate(payload)
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
        if File.exist?(session_path)
          sync_state = session_synchronizer.inspect_if_available(session_path)
          if sync_state
            payload[:session_sync] = {
              mode: sync_state.mode,
              revision: sync_state.revision,
              error: sync_state.error,
              gateway_busy: rpc_clients.busy?(session_path)
            }
          end
        else
          payload[:session_sync] = { mode: :available, revision: nil, error: nil, gateway_busy: rpc_clients.busy?(session_path) }
        end
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
        session_path = canonical_rpc_session_path(session_path)
        halt 404 unless command_session_available?(session_path)

        content_type :json
        live_status = live_session_status(session_path)
        status = if live_status&.fetch(:disk_independent)
          PiSessionStore::Status.new
        elsif File.exist?(session_path)
          PiSessionStore.new(root: settings.sessions_root).status(session_path)
        else
          PiSessionStore::Status.new
        end
        status = apply_live_session_status(status, live_status)
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

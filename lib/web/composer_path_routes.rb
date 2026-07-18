require "json"
require_relative "../suggest_composer_paths"

module Web
  module ComposerPathRoutes
    SESSION_PATH_BYTES = 16 * 1_024

    module Helpers
      private

      def composer_session_cwd(raw_session_path)
        return unless raw_session_path.is_a?(String) && raw_session_path.valid_encoding?
        return if raw_session_path.empty? || raw_session_path.bytesize > SESSION_PATH_BYTES || raw_session_path.include?("\0")
        return unless File.expand_path(raw_session_path) == raw_session_path

        require_current_workspace_session!(raw_session_path)
        pending_rpc_cwd(raw_session_path) || session_cwd(raw_session_path)
      rescue ArgumentError, EncodingError
        nil
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.post "/composer/path_suggestions" do
        mode = params["mode"].to_s
        query = params["query"]
        halt 400, "Invalid suggestion mode" unless ["fuzzy", "path"].include?(mode)
        halt 400, "Invalid query" unless query.is_a?(String) && query.valid_encoding? && query.bytesize <= SuggestComposerPaths::QUERY_BYTES && !query.include?("\0")

        cwd = composer_session_cwd(params["session"])
        halt 404 unless cwd

        headers "Cache-Control" => "no-store"
        content_type :json
        JSON.generate(suggestions: SuggestComposerPaths.call(cwd, mode, query))
      end
    end
  end
end

require "json"
require "openssl"
require_relative "../workspace_secret_store"
require_relative "../workspace_access_store"
require_relative "../workspace_session_ownership_store"

module Web
  module WorkspaceAccess
    WORKSPACE_ENDPOINTS = %w[
      /workspace-key
      /workspace-access/status
      /workspace-access/pending
      /workspace-access/approve
      /workspace-access/deny
    ].freeze
    WORKSPACE_COOKIE = "pi_gateway_workspace".freeze
    STORE_CACHE = {}
    STORE_CACHE_MUTEX = Mutex.new

    module Helpers
      private

      def multi_user_mode?
        !!settings.multi_user_mode
      end

      def current_workspace_id
        return unless multi_user_mode?

        request.cookies[WorkspaceAccess::WORKSPACE_COOKIE].to_s
      end

      def enforce_workspace_access
        return unless multi_user_mode?
        return if WorkspaceAccess::WORKSPACE_ENDPOINTS.include?(request.path_info)
        return unless approved_browser? || !browser_access_enabled?
        return if approved_current_workspace?

        @workspace_key_error = nil
        @workspace_return_to = request.fullpath
        @workspace_bootstrap_required = workspace_bootstrap_required?
        status 403
        halt erb(:workspace_key)
      end

      def workspace_secret
        @workspace_secret ||= WorkspaceSecretStore.new(path: settings.workspace_secret_path).secret
      end

      def workspace_access_store
        path = settings.workspace_access_path
        WorkspaceAccess::STORE_CACHE_MUTEX.synchronize do
          WorkspaceAccess::STORE_CACHE[path] ||= WorkspaceAccessStore.new(path: path)
        end
      end

      def workspace_session_ownership_store
        path = settings.workspace_ownership_path
        WorkspaceAccess::STORE_CACHE_MUTEX.synchronize do
          WorkspaceAccess::STORE_CACHE[path] ||= WorkspaceSessionOwnershipStore.new(path: path)
        end
      end

      def workspace_id_for_key(key)
        OpenSSL::HMAC.hexdigest("SHA256", workspace_secret, normalize_workspace_key(key))
      end

      def normalize_workspace_key(key)
        key.to_s.strip
      end

      def valid_workspace_key?(key)
        normalized = normalize_workspace_key(key)
        return false if normalized.length < 12

        classes = 0
        classes += 1 if normalized.match?(/[a-z]/)
        classes += 1 if normalized.match?(/[A-Z]/)
        classes += 1 if normalized.match?(/[0-9]/)
        classes += 1 if normalized.match?(/[^a-zA-Z0-9]/)
        classes >= 3
      end

      def approved_current_workspace?
        workspace_access_store.approved?(current_workspace_id)
      end

      def workspace_bootstrap_required?
        !workspace_access_store.any_approved?
      end

      def workspace_approval_allowed?
        (approved_browser? || !browser_access_enabled?) && approved_current_workspace?
      end

      def set_workspace_cookie(workspace_id)
        response.set_cookie(
          WorkspaceAccess::WORKSPACE_COOKIE,
          value: workspace_id,
          path: "/",
          httponly: true,
          same_site: :lax,
          max_age: 365 * 24 * 60 * 60
        )
      end

      def require_current_workspace_session!(session_path)
        return session_path unless multi_user_mode?

        halt 404 unless workspace_session_ownership_store.owned_by?(session_path, current_workspace_id)
        session_path
      end

      def require_current_workspace_session_hash!(session_hash)
        return session_hash unless multi_user_mode?

        halt 404 unless workspace_session_ownership_store.owns_session_hash?(session_hash, current_workspace_id)
        session_hash
      end

      def claim_session_for_current_workspace(session_path)
        return session_path unless multi_user_mode?

        workspace_session_ownership_store.claim(session_path, current_workspace_id)
        session_path
      end

      def workspace_session_filter
        return nil unless multi_user_mode?

        workspace_id = current_workspace_id
        ->(session) { workspace_session_ownership_store.owned_by?(session.path, workspace_id) }
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.post "/workspace-key" do
        halt 404 unless multi_user_mode?
        halt 403 unless approved_browser? || !browser_access_enabled?

        key = params["workspace_key"].to_s
        unless valid_workspace_key?(key)
          @workspace_key_error = "Use at least 12 characters and include at least 3 of: lowercase letters, uppercase letters, numbers, symbols."
          @workspace_return_to = safe_return_to
          @workspace_bootstrap_required = workspace_bootstrap_required?
          status 403
          erb :workspace_key
        else
          workspace_id = workspace_id_for_key(key)
          if workspace_access_store.approved?(workspace_id)
            set_workspace_cookie(workspace_id)
            redirect safe_return_to
          elsif workspace_bootstrap_required?
            if secure_compare(params["admin_password"].to_s, settings.gateway_admin_password.to_s)
              workspace_access_store.approve_workspace(workspace_id)
              set_workspace_cookie(workspace_id)
              redirect safe_return_to
            else
              @workspace_key_error = "Admin password did not match."
              @workspace_return_to = safe_return_to
              @workspace_bootstrap_required = true
              status 403
              erb :workspace_key
            end
          else
            @workspace_pending_request = workspace_access_store.request_access(workspace_id, browser_token: browser_token)
            @workspace_return_to = safe_return_to
            status 403
            erb :workspace_key
          end
        end
      end

      app.get "/workspace-access/status" do
        halt 404 unless multi_user_mode?
        halt 403 unless approved_browser? || !browser_access_enabled?

        request = workspace_access_store.request_for_code(params["code"].to_s)
        status_value = if request && workspace_access_store.approved?(request["workspace_id"])
          set_workspace_cookie(request.fetch("workspace_id"))
          "approved"
        elsif request && request["denied_at"]
          "denied"
        elsif request
          "pending"
        else
          "unknown"
        end

        content_type :json
        JSON.generate(status: status_value)
      end

      app.get "/workspace-access/pending" do
        halt 404 unless multi_user_mode?
        halt 403 unless workspace_approval_allowed?

        content_type :json
        JSON.generate(requests: workspace_access_store.pending_requests)
      end

      app.post "/workspace-access/approve" do
        halt 404 unless multi_user_mode?
        halt 403 unless workspace_approval_allowed?
        halt 400, "Code is required" if params["code"].to_s.empty?

        request = workspace_access_store.approve_code(params.fetch("code"))
        content_type :json
        JSON.generate(ok: !request.nil?)
      end

      app.post "/workspace-access/deny" do
        halt 404 unless multi_user_mode?
        halt 403 unless workspace_approval_allowed?
        halt 400, "Code is required" if params["code"].to_s.empty?

        request = workspace_access_store.deny_code(params.fetch("code"))
        content_type :json
        JSON.generate(ok: !request.nil?)
      end
    end
  end
end

require "json"
require "securerandom"
require_relative "../browser_access_store"

module Web
  module BrowserAccess
    ACCESS_ENDPOINTS = %w[
      /browser-access/request
      /browser-access/admin-login
      /browser-access/status
      /browser-access/pending
      /browser-access/approve
      /browser-access/deny
    ].freeze
    BROWSER_ACCESS_STORE_CACHE = {}
    BROWSER_ACCESS_STORE_CACHE_MUTEX = Mutex.new

    module Helpers
      private

      def browser_access_enabled?
        !settings.gateway_admin_password.to_s.empty?
      end

      def browser_access_store
        path = settings.browser_access_path
        BrowserAccess::BROWSER_ACCESS_STORE_CACHE_MUTEX.synchronize do
          BrowserAccess::BROWSER_ACCESS_STORE_CACHE[path] ||= BrowserAccessStore.new(path: path)
        end
      end

      def browser_token
        return @browser_token if defined?(@browser_token)

        @browser_token = request.cookies["pi_gateway_browser"]
        return @browser_token unless @browser_token.to_s.empty?

        @browser_token = SecureRandom.hex(32)
        response.set_cookie("pi_gateway_browser", value: @browser_token, path: "/", httponly: true, same_site: :lax, max_age: 365 * 24 * 60 * 60)
        @browser_token
      end

      def approved_browser?
        browser_access_enabled? && browser_access_store.approved?(browser_token)
      end

      def enforce_browser_access
        return unless browser_access_enabled?
        return if BrowserAccess::ACCESS_ENDPOINTS.include?(request.path_info)
        return if approved_browser?

        @access_request = browser_access_store.ensure_pending(token: browser_token, ip: request.ip, user_agent: request.user_agent)
        @access_error = nil
        status 403
        halt erb(:access_blocked)
      end

      def safe_return_to
        return_to = params["return_to"].to_s
        return return_to if return_to.start_with?("/") && !return_to.start_with?("//")

        "/"
      end

      def secure_compare(left, right)
        return false if left.empty? || right.empty?
        return false unless left.bytesize == right.bytesize

        Rack::Utils.secure_compare(left, right)
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.post "/browser-access/request" do
        halt 404 unless browser_access_enabled?

        browser_access_store.request_access(browser_token, ip: request.ip, user_agent: request.user_agent)
        redirect safe_return_to
      end

      app.post "/browser-access/admin-login" do
        halt 404 unless browser_access_enabled?

        if secure_compare(params["password"].to_s, settings.gateway_admin_password.to_s)
          browser_access_store.approve_current_browser(browser_token, label: request.user_agent)
          redirect safe_return_to
        else
          @access_request = browser_access_store.ensure_pending(token: browser_token, ip: request.ip, user_agent: request.user_agent)
          @access_error = "Admin password did not match."
          status 403
          erb :access_blocked
        end
      end

      app.get "/browser-access/status" do
        halt 404 unless browser_access_enabled?

        content_type :json
        JSON.generate(status: browser_access_store.pending_status(browser_token))
      end

      app.get "/browser-access/pending" do
        halt 403 unless approved_browser?

        content_type :json
        JSON.generate(requests: browser_access_store.pending_requests)
      end

      app.post "/browser-access/approve" do
        halt 403 unless approved_browser?

        halt 400, "Code is required" if params["code"].to_s.empty?

        request = browser_access_store.approve_code(params.fetch("code"))
        content_type :json
        JSON.generate(ok: !request.nil?)
      end

      app.post "/browser-access/deny" do
        halt 403 unless approved_browser?

        halt 400, "Code is required" if params["code"].to_s.empty?

        request = browser_access_store.deny_code(params.fetch("code"))
        content_type :json
        JSON.generate(ok: !request.nil?)
      end
    end
  end
end

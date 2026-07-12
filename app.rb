require "sinatra/base"
require "json"
require "securerandom"
require_relative "lib/rpc/pending_session_registry"
require_relative "lib/sessions/session_family"
require_relative "lib/sessions/sidebar"
require "ipaddr"
require_relative "lib/pi_session_store"
require_relative "lib/configured_session_cwds"
require_relative "lib/web/view_helpers"
require_relative "lib/web/store_helpers"
require_relative "lib/web/rpc_helpers"
require_relative "lib/web/browser_access"
require_relative "lib/web/workspace_access"
require_relative "lib/web/pwa_routes"
require_relative "lib/web/gateway_update_routes"
require_relative "lib/web/session_view_routes"
require_relative "lib/web/session_action_routes"
require_relative "lib/pi_rpc_client"
require_relative "lib/gateway_updater"
require_relative "lib/gateway_update_coordinator"
require_relative "lib/request_gateway_restart"

class PiWebGateway < Sinatra::Base
  register Web::BrowserAccess
  register Web::WorkspaceAccess
  register Web::PwaRoutes
  register Web::GatewayUpdateRoutes
  register Web::SessionViewRoutes
  register Web::SessionActionRoutes

  set :root, File.dirname(__FILE__)
  set :public_folder, File.join(root, "public")
  set :static, true
  set :static_cache_control, [:no_cache]
  set :sessions_root, ENV.fetch("PI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :attachments_root, ENV.fetch("PI_ATTACHMENTS_ROOT", File.expand_path("~/.pi/web-gateway/attachments"))
  GATEWAY_ENV_PATH = ENV.fetch("PI_GATEWAY_ENV_PATH", File.expand_path("~/.config/pi-web-gateway/env"))

  def self.load_gateway_env_file
    return unless File.exist?(GATEWAY_ENV_PATH)

    File.readlines(GATEWAY_ENV_PATH).each do |line|
      next if line.strip.empty? || line.strip.start_with?("#")

      key, value = line.split("=", 2)
      next unless key && value
      next if ENV.key?(key)

      ENV[key] = value.strip.sub(/\A(['"])(.*)\1\z/, "\\2")
    end
  end

  def self.permitted_hosts_from_env
    ENV.fetch("PI_GATEWAY_PERMITTED_HOSTS", "")
      .split(",")
      .map(&:strip)
      .reject(&:empty?)
  end

  def self.development_permitted_hosts
    [
      "localhost",
      ".localhost",
      ".test",
      IPAddr.new("0.0.0.0/0"),
      IPAddr.new("::/0")
    ]
  end

  load_gateway_env_file
  set :session_cwds_path, ENV.fetch("PI_SESSION_CWDS_PATH", ConfiguredSessionCwds.default_path)
  set :host_authorization, lambda {
    configured_hosts = permitted_hosts_from_env
    if development?
      { permitted_hosts: development_permitted_hosts + configured_hosts }
    elsif configured_hosts.empty?
      {}
    else
      { permitted_hosts: configured_hosts }
    end
  }

  browser_auth_disabled = ENV.fetch("PI_BROWSER_AUTH_DISABLED", "").match?(/\A(?:1|true|yes|on)\z/i)
  multi_user_mode = ENV.fetch("PI_MULTI_USER_MODE", "").match?(/\A(?:1|true|yes|on)\z/i)
  gateway_admin_password = ENV["PI_GATEWAY_ADMIN_PASSWORD"].to_s
  if gateway_admin_password.empty? && !browser_auth_disabled
    raise "PI_GATEWAY_ADMIN_PASSWORD is required. Set it in #{GATEWAY_ENV_PATH} or in the gateway process environment."
  end

  set :read_state_path, ENV.fetch("PI_READ_STATE_PATH", File.expand_path("~/.pi/web-gateway/read-state.json"))
  set :browser_access_path, ENV.fetch("PI_BROWSER_ACCESS_PATH", File.expand_path("~/.pi/web-gateway/browser-access.json"))
  set :browser_auth_disabled, browser_auth_disabled
  set :multi_user_mode, multi_user_mode
  set :workspace_secret_path, ENV.fetch("PI_WORKSPACE_SECRET_PATH", File.expand_path("~/.pi/web-gateway/workspace-secret"))
  set :workspace_access_path, ENV.fetch("PI_WORKSPACE_ACCESS_PATH", File.expand_path("~/.pi/web-gateway/workspace-access.json"))
  set :workspace_ownership_path, ENV.fetch("PI_WORKSPACE_OWNERSHIP_PATH", File.expand_path("~/.pi/web-gateway/session-owners.json"))
  set :gateway_admin_password, gateway_admin_password
  pi_rpc_command_prefix = PiRpcClient.command_prefix(node_path: ENV["PI_GATEWAY_NODE"], pi_path: ENV["PI_GATEWAY_PI"])
  set :pi_rpc_command_prefix, pi_rpc_command_prefix
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path, command_prefix: pi_rpc_command_prefix) }]
  set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd, command_prefix: pi_rpc_command_prefix) }]
  set :rpc_client_registry, nil
  set :gateway_instance_id, SecureRandom.hex(16)
  set :gateway_update_coordinator, GatewayUpdateCoordinator.new(
    updater: GatewayUpdater.new(root),
    restarter: -> { RequestGatewayRestart.call(PiWebGateway.settings.rpc_client_registry) }
  )
  set :pending_session_registry, Rpc::PendingSessionRegistry.new
  set :rpc_idle_timeout_seconds, ENV.fetch("PI_RPC_IDLE_TIMEOUT_SECONDS", "1800").to_i

  RECENT_SIDEBAR_SESSION_LIMIT = Sessions::Sidebar::RECENT_SESSION_LIMIT
  SIDEBAR_SESSION_PAGE_SIZE = Sessions::Sidebar::SESSION_PAGE_SIZE

  helpers Web::ViewHelpers
  helpers Web::StoreHelpers
  helpers Web::RpcHelpers

  before do
    enforce_browser_access
    enforce_workspace_access
    cleanup_idle_rpc_clients
  end
end

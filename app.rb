require "sinatra/base"
require "rack/deflater"
require "json"
require "securerandom"
require_relative "lib/rpc/pending_session_registry"
require_relative "lib/rpc/idle_client_maintenance"
require_relative "lib/sessions/session_family"
require_relative "lib/sessions/sidebar"
require_relative "lib/sessions/session_synchronizer"
require "ipaddr"
require_relative "lib/pi_session_store"
require_relative "lib/configured_session_cwds"
require_relative "lib/web/view_helpers"
require_relative "lib/web/store_helpers"
require_relative "lib/web/rpc_helpers"
require_relative "lib/web/request_transport_security"
require_relative "lib/web/security_headers"
require_relative "lib/web/request_origin_protection"
require_relative "lib/web/browser_access"
require_relative "lib/web/workspace_access"
require_relative "lib/web/pwa_routes"
require_relative "lib/web/gateway_update_routes"
require_relative "lib/web/resource_usage_routes"
require_relative "lib/web/session_view_routes"
require_relative "lib/web/session_action_routes"
require_relative "lib/web/composer_path_routes"
require_relative "lib/pi_rpc_client"
require_relative "lib/gateway_updater"
require_relative "lib/gateway_update_coordinator"
require_relative "lib/request_gateway_restart"
require_relative "lib/request_rate_limiter"
require_relative "lib/resource_usage_monitor"
require_relative "lib/friendly_host_authorization"

class Gripi < Sinatra::Base
  use Rack::Deflater,
    include: ["application/json"],
    if: ->(_env, _status, headers, _body) { headers["content-length"].to_i >= 1_024 }

  register Web::SecurityHeaders
  register Web::RequestTransportSecurity
  register Web::RequestOriginProtection
  register Web::BrowserAccess
  register Web::WorkspaceAccess
  register Web::PwaRoutes
  register Web::GatewayUpdateRoutes
  register Web::ResourceUsageRoutes
  register Web::SessionViewRoutes
  register Web::SessionActionRoutes
  register Web::ComposerPathRoutes

  set :root, File.dirname(__FILE__)
  set :public_folder, File.join(root, "public")
  set :static, true
  set :static_cache_control, [:no_cache]
  set :sessions_root, ENV.fetch("GRIPI_SESSIONS_ROOT", File.expand_path("~/.pi/agent/sessions"))
  set :attachments_root, ENV.fetch("GRIPI_ATTACHMENTS_ROOT", File.expand_path("~/.pi/gripi/attachments"))
  GRIPI_ENV_PATH = ENV.fetch("GRIPI_ENV_PATH", File.expand_path("~/.config/gripi/env"))

  def self.load_gateway_env_file
    return unless File.exist?(GRIPI_ENV_PATH)

    File.readlines(GRIPI_ENV_PATH).each do |line|
      next if line.strip.empty? || line.strip.start_with?("#")

      key, value = line.split("=", 2)
      next unless key && value
      next if ENV.key?(key)

      ENV[key] = value.strip.sub(/\A(['"])(.*)\1\z/, "\\2")
    end
  end

  def self.normalized_permitted_host(value)
    value = value.to_s.strip
    return if value.empty?

    if (match = value.match(/\A\[([^\]]+)\](?::\d+)?\z/))
      address = IPAddr.new(match[1])
      return "[#{address}]" if address.ipv6?
    end

    begin
      address = IPAddr.new(value)
      return address.ipv6? ? "[#{address}]" : address.to_s
    rescue IPAddr::InvalidAddressError
      uri = URI.parse("http://#{value}")
      return unless uri.host && uri.userinfo.nil? && uri.path.empty? && uri.query.nil? && uri.fragment.nil?

      uri.host.downcase
    end
  rescue IPAddr::InvalidAddressError, URI::InvalidURIError
    nil
  end

  def self.normalized_suggested_host(value)
    host = normalized_permitted_host(value)
    return if host.to_s.start_with?(".") || host.to_s.include?("*")
    return if ["0.0.0.0", "[::]"].include?(host)

    host
  end

  def self.production_permitted_hosts
    bind_host = normalized_permitted_host(ENV.fetch("GRIPI_BIND_HOST", "127.0.0.1"))
    hosts = [bind_host].compact.reject { |host| ["0.0.0.0", "[::]"].include?(host) }
    hosts.concat(["localhost", ".localhost"]) if bind_host == "localhost" || loopback_host?(bind_host)
    hosts.concat(
      ENV.fetch("GRIPI_PERMITTED_HOSTS", "").split(",").filter_map { |host| normalized_permitted_host(host) }
    )
    hosts.compact.uniq
  end

  def self.loopback_host?(host)
    IPAddr.new(host.to_s.delete_prefix("[").delete_suffix("]")).loopback?
  rescue IPAddr::InvalidAddressError
    false
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
  browser_auth_disabled = ENV.fetch("GRIPI_BROWSER_AUTH_DISABLED", "").match?(/\A(?:1|true|yes|on)\z/i)
  allow_insecure_remote_http = ENV.fetch("GRIPI_ALLOW_INSECURE_REMOTE_HTTP", "").match?(/\A(?:1|true|yes|on)\z/i)

  set :session_cwds_path, ENV.fetch("GRIPI_SESSION_CWDS_PATH", ConfiguredSessionCwds.default_path)
  set :host_authorization, lambda {
    permitted_hosts = production_permitted_hosts
    permitted_hosts = development_permitted_hosts + permitted_hosts if development? || test?
    {
      permitted_hosts: permitted_hosts,
      deny_all: permitted_hosts.empty?,
      normalize_suggested_host: ->(host) { normalized_suggested_host(host) },
      configured_hosts_present: !ENV.fetch("GRIPI_PERMITTED_HOSTS", "").empty?
    }
  }
  set :trust_proxy_headers, ENV.fetch("GRIPI_TRUST_PROXY_HEADERS", "").match?(/\A(?:1|true|yes|on)\z/i)
  set :enforce_secure_remote_transport, production? && !allow_insecure_remote_http

  multi_user_mode = ENV.fetch("GRIPI_MULTI_USER_MODE", "").match?(/\A(?:1|true|yes|on)\z/i)
  auto_approve_projects = ENV.fetch("GRIPI_AUTO_APPROVE_PROJECTS", "1").match?(/\A(?:1|true|yes|on)\z/i)
  gateway_admin_password = ENV["GRIPI_ADMIN_PASSWORD"].to_s
  if gateway_admin_password.empty? && !browser_auth_disabled
    raise "GRIPI_ADMIN_PASSWORD is required. Set it in #{GRIPI_ENV_PATH} or in the gateway process environment."
  end

  set :read_state_path, ENV.fetch("GRIPI_READ_STATE_PATH", File.expand_path("~/.pi/gripi/read-state.json"))
  set :pinned_sessions_path, File.expand_path("~/.pi/gripi/pinned-sessions.json")
  set :browser_access_path, ENV.fetch("GRIPI_BROWSER_ACCESS_PATH", File.expand_path("~/.pi/gripi/browser-access.json"))
  set :browser_auth_disabled, browser_auth_disabled
  set :multi_user_mode, multi_user_mode
  set :workspace_secret_path, ENV.fetch("GRIPI_WORKSPACE_SECRET_PATH", File.expand_path("~/.pi/gripi/workspace-secret"))
  set :workspace_access_path, ENV.fetch("GRIPI_WORKSPACE_ACCESS_PATH", File.expand_path("~/.pi/gripi/workspace-access.json"))
  set :workspace_ownership_path, ENV.fetch("GRIPI_WORKSPACE_OWNERSHIP_PATH", File.expand_path("~/.pi/gripi/session-owners.json"))
  set :gateway_admin_password, gateway_admin_password
  pi_rpc_command_prefix = PiRpcClient.command_prefix(node_path: ENV["GRIPI_NODE"], pi_path: ENV["GRIPI_PI"])
  pi_rpc_command_prefix += ["--approve"] if auto_approve_projects
  set :pi_rpc_command_prefix, pi_rpc_command_prefix
  set :rpc_client_factory, [->(session_path) { PiRpcClient.start(session_path, command_prefix: pi_rpc_command_prefix) }]
  set :new_rpc_client_factory, [->(cwd) { PiRpcClient.start_in_cwd(cwd, command_prefix: pi_rpc_command_prefix) }]
  set :rpc_client_registry, nil
  set :rpc_client_registry_mutex, Mutex.new
  set :session_synchronizer, nil
  set :session_synchronizer_mutex, Mutex.new
  set :gateway_instance_id, SecureRandom.hex(16)
  set :gateway_update_coordinator, GatewayUpdateCoordinator.new(
    updater: GatewayUpdater.new(root),
    restarter: -> { RequestGatewayRestart.call(Gripi.settings.rpc_client_registry) },
    active_session_count: -> { Gripi.settings.rpc_client_registry&.busy_session_count.to_i }
  )
  set :pending_session_registry, Rpc::PendingSessionRegistry.new
  def self.setup_host_authorization(builder)
    builder.use FriendlyHostAuthorization, host_authorization
  end

  rpc_idle_timeout_seconds = begin
    Integer(ENV.fetch("GRIPI_RPC_IDLE_TIMEOUT_SECONDS", "300"), 10)
  rescue ArgumentError
    raise ArgumentError, "GRIPI_RPC_IDLE_TIMEOUT_SECONDS must be a non-negative integer"
  end
  raise ArgumentError, "GRIPI_RPC_IDLE_TIMEOUT_SECONDS must be a non-negative integer" if rpc_idle_timeout_seconds.negative?

  rpc_idle_sweep_seconds = begin
    Float(ENV.fetch("GRIPI_RPC_IDLE_SWEEP_SECONDS", "30"))
  rescue ArgumentError
    raise ArgumentError, "GRIPI_RPC_IDLE_SWEEP_SECONDS must be a positive number"
  end
  unless rpc_idle_sweep_seconds.positive? && rpc_idle_sweep_seconds.finite?
    raise ArgumentError, "GRIPI_RPC_IDLE_SWEEP_SECONDS must be a positive number"
  end

  set :rpc_idle_timeout_seconds, rpc_idle_timeout_seconds
  set :rpc_idle_sweep_seconds, rpc_idle_sweep_seconds
  set :resource_monitoring_enabled, ENV.fetch("GRIPI_RESOURCE_MONITORING", "").match?(/\A(?:1|true|yes|on)\z/i)
  set :resource_usage_monitor, ResourceUsageMonitor.new
  set :access_request_rate_limiter, RequestRateLimiter.new(limit: 30, window: 60)
  set :admin_login_rate_limiter, RequestRateLimiter.new(limit: 10, window: 5 * 60)

  RECENT_SIDEBAR_SESSION_LIMIT = Sessions::Sidebar::RECENT_SESSION_LIMIT
  SIDEBAR_SESSION_PAGE_SIZE = Sessions::Sidebar::SESSION_PAGE_SIZE

  helpers Web::ViewHelpers
  helpers Web::StoreHelpers
  helpers Web::RpcHelpers

  def self.session_synchronizer_for(registry)
    synchronizer = settings.session_synchronizer
    return synchronizer if synchronizer&.configured_for?(settings.sessions_root, registry)

    settings.session_synchronizer_mutex.synchronize do
      synchronizer = settings.session_synchronizer
      unless synchronizer&.configured_for?(settings.sessions_root, registry)
        synchronizer = Sessions::SessionSynchronizer.new(sessions_root: settings.sessions_root, rpc_clients: registry)
        settings.session_synchronizer = synchronizer
      end
      synchronizer
    end
  end

  def self.cleanup_idle_rpc_clients
    timeout = settings.rpc_idle_timeout_seconds
    registry = settings.rpc_client_registry
    return [] unless timeout.positive? && registry

    synchronizer = session_synchronizer_for(registry)
    closed_paths = []
    errors = []
    registry.idle_client_paths(idle_timeout: timeout).each do |candidate_path|
      begin
        session_path, remapped = remap_idle_pending_rpc_client(candidate_path, registry)
        next unless session_path

        closed = false
        if File.exist?(session_path)
          synchronizer.reconcile_if_available(session_path) do
            closed = if remapped
              registry.close_client_if_idle(session_path)
            else
              registry.close_client_if_expired(session_path, idle_timeout: timeout)
            end
          end
        else
          closed = registry.close_client_if_expired(session_path, idle_timeout: timeout)
        end
        if closed
          settings.pending_session_registry.forget(session_path)
          closed_paths << session_path
        end
      rescue StandardError => error
        errors << error
      end
    end
    raise errors.first if errors.any?

    closed_paths
  end

  def self.remap_idle_pending_rpc_client(session_path, registry)
    cwd = settings.pending_session_registry.cwd_for(session_path)
    return [session_path, false] unless cwd

    response = registry.with_existing_client(session_path, touch: false) { |client| client.get_state }
    data = response.is_a?(Hash) && response["data"].is_a?(Hash) ? response["data"] : response
    persisted_path = data["sessionFile"] || data["session_file"] || data["path"] if data.is_a?(Hash)
    return [session_path, false] unless persisted_path
    return [nil, false] unless File.exist?(persisted_path)
    return [nil, false] unless PiSessionStore.new(root: settings.sessions_root).cwd_for_session(persisted_path) == cwd

    registry.move(session_path, persisted_path)
    PiAttachmentStore.new(root: settings.attachments_root).migrate_session(session_path, persisted_path)
    WorkspaceSessionOwnershipStore.new(path: settings.workspace_ownership_path).move(session_path, persisted_path)
    settings.pending_session_registry.forget(session_path)
    [persisted_path, true]
  rescue PiRpcClientRegistry::OperationPending, PiRpcClientRegistry::ClientRetiring, PiRpcClientRegistry::ClientStarting,
    PiRpcClient::RequestTimeout, IOError, SystemCallError
    [nil, false]
  end

  set :rpc_idle_client_maintenance, Rpc::IdleClientMaintenance.new(
    interval: rpc_idle_sweep_seconds,
    cleanup: -> { Gripi.cleanup_idle_rpc_clients },
    on_error: ->(error) { warn("Idle Pi cleanup failed: #{error.class}: #{error.message}") }
  )

  def self.start_rpc_client_maintenance
    settings.rpc_idle_client_maintenance.start
  end

  def self.stop_rpc_client_maintenance
    settings.rpc_idle_client_maintenance.stop
  ensure
    settings.rpc_client_registry&.close_all
  end

  before do
    enforce_browser_access
    enforce_workspace_access
  end
end

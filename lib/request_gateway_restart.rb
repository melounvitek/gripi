require "fileutils"
require "tempfile"

class RequestGatewayRestart
  def self.call(rpc_client_registry = nil, env: ENV)
    new.call(rpc_client_registry, env: env)
  end

  def initialize(shutdown: -> { Process.kill("TERM", Process.pid) })
    @shutdown = shutdown
  end

  def call(rpc_client_registry = nil, env: ENV)
    restart_path = env["PI_GATEWAY_RESTART_PATH"]
    restart_path = default_restart_path(env) if restart_path.nil? || restart_path.empty?
    create_marker(restart_path)
    cleanup_error = begin
      rpc_client_registry&.close_all
      nil
    rescue StandardError => error
      error
    end

    begin
      @shutdown.call
    rescue StandardError
      FileUtils.rm_f(restart_path)
      raise
    end
    raise cleanup_error if cleanup_error
  end

  private

  def default_restart_path(env)
    home = env["HOME"]
    raise ArgumentError, "HOME or PI_GATEWAY_RESTART_PATH must be set" if home.nil? || home.empty?

    File.join(home, ".pi", "web-gateway", "restart-request")
  end

  def create_marker(path)
    directory = File.dirname(path)
    FileUtils.mkdir_p(directory)
    temporary = Tempfile.new(["#{File.basename(path)}.tmp-", ""], directory)
    temporary.close
    File.rename(temporary.path, path)
  ensure
    temporary&.close!
  end
end

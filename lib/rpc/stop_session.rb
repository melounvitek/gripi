require_relative "../pi_rpc_client"
require_relative "../pi_rpc_client_registry"

module Rpc
  class StopSession
    Result = Struct.new(:forced, :stopping, keyword_init: true)

    def self.call(&block)
      new.call(&block)
    end

    def call
      yield
      Result.new(forced: false, stopping: false)
    rescue PiRpcClientRegistry::InterruptPending, PiRpcClientRegistry::ClientRetiring, PiRpcClientRegistry::ClientStarting
      Result.new(forced: false, stopping: true)
    rescue PiRpcClient::RequestTimeout, Errno::EPIPE, IOError
      Result.new(forced: true, stopping: false)
    end
  end
end

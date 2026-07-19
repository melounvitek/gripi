require "json"
require "thread"
require "time"

module Rpc
  module Diagnostics
    MUTEX = Mutex.new

    def self.log(event, **fields)
      return unless ENV.fetch("GRIPI_RPC_DIAGNOSTICS", "").match?(/\A(?:1|true|yes|on)\z/i)

      payload = { component: "pi_rpc", event: event, timestamp: Time.now.utc.iso8601(3) }.merge(fields)
      MUTEX.synchronize { warn(JSON.generate(payload)) }
    rescue StandardError
      nil
    end
  end
end

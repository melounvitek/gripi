require "json"

module Web
  module GatewayUpdateRoutes
    module Helpers
      private

      def gateway_update_json(snapshot)
        JSON.generate(
          instanceId: settings.gateway_instance_id,
          state: snapshot.state&.to_s,
          reason: snapshot.reason&.to_s,
          message: snapshot.message,
          currentSha: snapshot.current_sha,
          targetSha: snapshot.target_sha,
          behindCount: snapshot.behind_count,
          summary: snapshot.summary
        )
      end
    end

    def self.registered(app)
      app.helpers Helpers

      app.get "/gateway-update" do
        headers "Cache-Control" => "no-store"
        content_type :json
        gateway_update_json(settings.gateway_update_coordinator.status)
      end

      app.post "/gateway-update" do
        snapshot = settings.gateway_update_coordinator.start
        status 202
        headers "Cache-Control" => "no-store"
        content_type :json
        gateway_update_json(snapshot)
      end
    end
  end
end

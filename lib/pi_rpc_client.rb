require "json"

class PiRpcClient
  def initialize(stdin:, stdout:)
    @stdin = stdin
    @stdout = stdout
  end

  def request(type, id:, **payload)
    command = payload.merge(id: id, type: type)
    @stdin.write(JSON.generate(command) + "\n")
    @stdin.flush if @stdin.respond_to?(:flush)

    each_response do |response|
      return response if response["id"] == id
    end

    nil
  end

  private

  def each_response
    while (line = @stdout.gets)
      next if line.strip.empty?

      begin
        yield JSON.parse(line)
      rescue JSON::ParserError
        next
      end
    end
  end
end

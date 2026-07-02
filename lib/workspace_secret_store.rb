require "fileutils"
require "securerandom"

class WorkspaceSecretStore
  def initialize(path:)
    @path = path
  end

  def secret
    existing = read_secret
    return existing unless existing.empty?

    generated = SecureRandom.hex(32)
    FileUtils.mkdir_p(File.dirname(@path))
    File.write(@path, generated + "\n", mode: "w", perm: 0o600)
    generated
  end

  private

  def read_secret
    return "" unless File.exist?(@path)

    File.read(@path).strip
  end
end

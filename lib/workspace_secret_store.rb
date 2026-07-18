require "securerandom"
require_relative "secure_state_file"

class WorkspaceSecretStore
  def initialize(path:)
    @file = SecureStateFile.new(path)
  end

  def secret
    existing = read_secret
    return existing unless existing.empty?

    generated = SecureRandom.hex(32)
    return generated if @file.create_once(generated + "\n")

    read_secret.tap { |secret| raise "Workspace secret file is empty" if secret.empty? }
  end

  private

  def read_secret
    @file.read.to_s.strip
  end
end

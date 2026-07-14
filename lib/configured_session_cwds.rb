class ConfiguredSessionCwds
  DEFAULT_PATH = "~/.config/gripi/pinned-dirs"
  LEGACY_DEFAULT_PATH = "~/.config/gripi/session-cwds.txt"

  def self.default_path
    default_path = File.expand_path(DEFAULT_PATH)
    legacy_default_path = File.expand_path(LEGACY_DEFAULT_PATH)
    return legacy_default_path if !File.exist?(default_path) && File.exist?(legacy_default_path)

    default_path
  end

  def self.read(path)
    new(path).read
  end

  def initialize(path)
    @path = path
  end

  def read
    return [] unless @path && File.file?(@path)

    File.readlines(@path).filter_map { |line| canonical_directory(line) }.uniq
  rescue SystemCallError
    []
  end

  private

  def canonical_directory(line)
    path = line.sub(/\s+#.*/, "").strip
    return if path.empty?

    expanded_path = File.expand_path(path)
    return unless File.directory?(expanded_path)
    return unless File.readable?(expanded_path) && File.executable?(expanded_path)

    File.realpath(expanded_path)
  rescue ArgumentError, SystemCallError
    nil
  end
end

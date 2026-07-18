require "fileutils"
require "tempfile"

class SecureStateFile
  FILE_MODE = 0o600
  DIRECTORY_MODE = 0o700

  def self.default_directory
    File.expand_path("~/.pi/gripi")
  end

  def initialize(path)
    @path = path
  end

  def read
    secure_default_directory
    return unless File.exist?(@path)

    File.open(@path, "r") do |file|
      file.chmod(FILE_MODE)
      file.read
    end
  end

  def write(content)
    with_prepared_tempfile(content) do |file|
      File.rename(file.path, @path)
    end
  end

  def create_once(content)
    created = false
    with_prepared_tempfile(content) do |file|
      begin
        File.link(file.path, @path)
        created = true
      rescue Errno::EEXIST
        nil
      end
    end
    created
  end

  private

  def with_prepared_tempfile(content)
    prepare_directory
    Tempfile.create([".#{File.basename(@path)}-", ".tmp"], File.dirname(@path)) do |file|
      file.chmod(FILE_MODE)
      file.write(content)
      file.flush
      file.fsync
      file.close
      yield file
    end
  end

  def prepare_directory
    directory = File.dirname(@path)
    if default_directory?(directory)
      FileUtils.mkdir_p(directory, mode: DIRECTORY_MODE)
      File.chmod(DIRECTORY_MODE, directory)
    else
      FileUtils.mkdir_p(directory)
    end
  end

  def secure_default_directory
    directory = File.dirname(@path)
    File.chmod(DIRECTORY_MODE, directory) if default_directory?(directory) && Dir.exist?(directory)
  end

  def default_directory?(directory)
    File.expand_path(directory) == File.expand_path(self.class.default_directory)
  end
end

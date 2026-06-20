module Sessions
  class SessionFamily
    def initialize(sessions)
      @sessions = sessions
    end

    def parent(session)
      session_by_path[normalized_path(session&.parent_session_path)]
    end

    def children(session)
      children_by_parent_path.fetch(normalized_path(session&.path), [])
    end

    def child_count(session)
      children(session).length
    end

    def root(session)
      current = session
      seen_paths = {}

      while current
        current_path = normalized_path(current.path)
        return current if seen_paths[current_path]

        seen_paths[current_path] = true
        parent_session = parent(current)
        return current unless parent_session

        current = parent_session
      end

      session
    end

    private

    def session_by_path
      @session_by_path ||= @sessions.each_with_object({}) do |session, index|
        path = normalized_path(session.path)
        index[path] = session if path
      end
    end

    def children_by_parent_path
      @children_by_parent_path ||= begin
        children = Hash.new { |hash, key| hash[key] = [] }

        @sessions.each do |session|
          parent_path = normalized_path(session.parent_session_path)
          children[parent_path] << session if parent_path && session_by_path.key?(parent_path)
        end

        children.transform_values { |siblings| newest_first(siblings) }
      end
    end

    def newest_first(sessions)
      sessions.sort_by { |session| session.modified_at || Time.at(0) }.reverse
    end

    def normalized_path(path)
      value = path.to_s
      return if value.empty?

      File.expand_path(value)
    end
  end
end

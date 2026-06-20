require "erb"
require "redcarpet"
require "nokogiri"
require "sanitize"

module Rendering
  class MarkdownRenderer < Redcarpet::Render::HTML
    ALLOWED_MARKDOWN_ELEMENTS = (Sanitize::Config::RELAXED[:elements] + %w[pre code span]).uniq.freeze
    ALLOWED_MARKDOWN_ATTRIBUTES = Sanitize::Config::RELAXED[:attributes].merge(
      "a" => (Sanitize::Config::RELAXED[:attributes]["a"] + %w[target rel]).uniq,
      "code" => ["class"],
      "span" => ["class"],
      "ol" => (Sanitize::Config::RELAXED[:attributes]["ol"] + %w[start]).uniq
    ).freeze

    HIGHLIGHT_LANGUAGE_ALIASES = {
      "bash" => "shell", "sh" => "shell", "shell" => "shell", "zsh" => "shell",
      "js" => "javascript", "javascript" => "javascript", "ts" => "javascript", "typescript" => "javascript",
      "json" => "json",
      "rb" => "ruby", "ruby" => "ruby"
    }.freeze

    HIGHLIGHT_PATTERNS = {
      "javascript" => [
        ["comment", %r{//[^\n]*|/\*.*?\*/}m],
        ["string", /`(?:\\.|[^`])*`|"(?:\\.|[^"])*"|'(?:\\.|[^'])*'/],
        ["number", /\b(?:0x[\da-f]+|\d+(?:\.\d+)?)\b/i],
        ["keyword", /\b(?:async|await|break|case|catch|class|const|continue|default|delete|do|else|export|extends|finally|for|from|function|if|import|in|instanceof|let|new|of|return|switch|throw|try|typeof|var|void|while|yield)\b/],
        ["literal", /\b(?:false|null|true|undefined)\b/]
      ],
      "json" => [
        ["key", /"(?:\\.|[^"])*"(?=\s*:)/],
        ["string", /"(?:\\.|[^"])*"/],
        ["number", /-?\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b/i],
        ["literal", /\b(?:false|null|true)\b/]
      ],
      "ruby" => [
        ["comment", /#[^\n]*/],
        ["string", /"(?:\\.|[^"])*"|'(?:\\.|[^'])*'/],
        ["symbol", /:\w+[!?=]?/],
        ["number", /\b\d+(?:\.\d+)?\b/],
        ["keyword", /\b(?:alias|and|begin|break|case|class|def|defined\?|do|else|elsif|end|ensure|false|for|if|in|module|next|nil|not|or|redo|rescue|retry|return|self|super|then|true|undef|unless|until|when|while|yield)\b/],
        ["function", /\b(?:puts|print|p|require|require_relative|attr_reader|attr_writer|attr_accessor)\b/]
      ],
      "shell" => [
        ["comment", /#[^\n]*/],
        ["string", /"(?:\\.|[^"])*"|'(?:\\.|[^'])*'/],
        ["variable", /\$\{?\w+\}?/],
        ["keyword", /\b(?:case|do|done|elif|else|esac|fi|for|function|if|in|then|until|while)\b/],
        ["function", /\b(?:awk|bundle|cd|cp|curl|echo|find|git|grep|mkdir|npm|rg|ruby|sed|yarn)\b/]
      ]
    }.freeze

    def block_code(code, language)
      normalized_language = normalized_highlight_language(language)
      return plain_code_block(code, language) unless normalized_language

      %(<pre><code class="highlight #{h(normalized_language)}">#{highlight_code(code, normalized_language)}</code></pre>\n)
    end

    def postprocess(full_document)
      Sanitize.fragment(
        continue_ordered_lists(full_document),
        elements: ALLOWED_MARKDOWN_ELEMENTS,
        attributes: ALLOWED_MARKDOWN_ATTRIBUTES,
        protocols: Sanitize::Config::RELAXED[:protocols]
      )
    end

    private

    def plain_code_block(code, language)
      class_attribute = safe_language_class(language)
      class_markup = class_attribute ? %( class="#{h(class_attribute)}") : ""

      %(<pre><code#{class_markup}>#{h(code)}</code></pre>\n)
    end

    def normalized_highlight_language(language)
      HIGHLIGHT_LANGUAGE_ALIASES[language.to_s.strip.downcase]
    end

    def highlight_code(code, language)
      patterns = HIGHLIGHT_PATTERNS.fetch(language)
      highlighted = +""
      remaining = code.to_s

      until remaining.empty?
        match = patterns.filter_map do |token_class, pattern|
          token = remaining.match(/\A#{pattern}/)
          [token_class, token[0]] if token
        end.first

        if match
          token_class, token = match
          highlighted << %(<span class="syntax-#{token_class}">#{h(token)}</span>)
          remaining = remaining[token.length..] || ""
        else
          highlighted << h(remaining[0])
          remaining = remaining[1..] || ""
        end
      end

      highlighted
    end

    def safe_language_class(language)
      language = language.to_s.strip
      return nil if language.empty?
      return nil unless language.match?(/\A[\w.+#-]+\z/)

      language
    end

    def h(value)
      ERB::Util.html_escape(value)
    end

    def continue_ordered_lists(full_document)
      fragment = Nokogiri::HTML5.fragment(full_document)
      next_ordered_list_start = nil

      fragment.children.each do |node|
        if whitespace_text?(node)
          next
        elsif code_block?(node)
          next
        elsif ordered_list?(node)
          item_count = node.element_children.count { |child| child.name == "li" }
          node["start"] = next_ordered_list_start.to_s if next_ordered_list_start
          next_ordered_list_start = (next_ordered_list_start || 1) + item_count
        else
          next_ordered_list_start = nil
        end
      end

      fragment.to_html
    end

    def ordered_list?(node)
      node.element? && node.name == "ol"
    end

    def code_block?(node)
      node.element? && node.name == "pre"
    end

    def whitespace_text?(node)
      node.text? && node.text.strip.empty?
    end
  end
end

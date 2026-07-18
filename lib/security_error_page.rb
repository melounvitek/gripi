require "erb"

class SecurityErrorPage
  CONTENT_SECURITY_POLICY = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'".freeze

  def self.render(title:, message:)
    new(title, message).render
  end

  def initialize(title, message)
    @title = title
    @message = message
  end

  def render
    <<~HTML
      <!doctype html>
      <html lang="en">
      <head>
        <meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <title>#{h(@title)} · Gripi</title>
        <style>
          :root { color-scheme: dark; --bg: #18181e; --panel: #1e1e24; --border: #505050b3; --text: #d4d4d4; --muted: #a0a0a6; --accent: #ff5a1f; --code: #17171d; }
          * { box-sizing: border-box; }
          body { min-height: 100vh; margin: 0; display: grid; place-items: center; padding: 1rem; color: var(--text); background-color: var(--bg); background-image: linear-gradient(#ffffff08 1px, transparent 1px), linear-gradient(90deg, #ffffff08 1px, transparent 1px), linear-gradient(#ffffff04 1px, transparent 1px), linear-gradient(90deg, #ffffff04 1px, transparent 1px); background-size: 96px 96px, 96px 96px, 12px 12px, 12px 12px; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
          main { width: min(42rem, 100%); padding: 1.4rem; border: 1px solid var(--border); border-radius: 0.5rem; background: var(--panel); box-shadow: 0 8px 26px #00000047; }
          h1 { margin: 0 0 1rem; font-family: "Commit Mono", "SFMono-Regular", ui-monospace, Menlo, Consolas, monospace; font-size: 1.15rem; }
          h1::before { content: "❯ "; color: var(--accent); }
          .message { margin: 0; padding: 1rem; border: 1px solid #50505066; border-radius: 0.35rem; color: var(--muted); background: var(--code); font-family: "Commit Mono", "SFMono-Regular", ui-monospace, Menlo, Consolas, monospace; font-size: 0.88rem; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
        </style>
      </head>
      <body>
        <main>
          <h1>#{h(@title)}</h1>
          <div class="message">#{h(@message)}</div>
        </main>
      </body>
      </html>
    HTML
  end

  private

  def h(value)
    ERB::Util.html_escape(value.to_s)
  end
end

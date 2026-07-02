ARG RUBY_VERSION=4.0.2
FROM ruby:${RUBY_VERSION}-bookworm

ARG NODE_MAJOR=24
ARG PI_VERSION=latest
ARG SESSION_NAMING_VERSION=0.2.1
ARG USER_ID=1000
ARG GROUP_ID=1000

ENV APP_HOME=/app \
    HOME=/home/piuser \
    BUNDLE_PATH=/usr/local/bundle \
    GEM_HOME=/usr/local/bundle \
    MISE_DATA_DIR=/home/piuser/.local/share/mise \
    MISE_CONFIG_DIR=/home/piuser/.config/mise \
    MISE_CACHE_DIR=/home/piuser/.cache/mise \
    PATH=/usr/local/bundle/bin:/home/piuser/.local/bin:/home/piuser/.local/share/mise/shims:$PATH

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    fd-find \
    git \
    gnupg \
    jq \
    less \
    openssh-client \
    procps \
    python3 \
    python3-pip \
    ripgrep \
    sudo \
    tini \
    unzip \
    xz-utils \
    build-essential \
  && install -d -m 0755 /etc/apt/keyrings \
  && curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg \
  && echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" > /etc/apt/sources.list.d/nodesource.list \
  && apt-get update \
  && apt-get install -y --no-install-recommends nodejs \
  && curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh \
  && npm install -g --ignore-scripts @earendil-works/pi-coding-agent@${PI_VERSION} \
  && if getent group "$GROUP_ID" >/dev/null; then group_name="$(getent group "$GROUP_ID" | cut -d: -f1)"; else groupadd --gid "$GROUP_ID" piuser && group_name=piuser; fi \
  && if getent passwd "$USER_ID" >/dev/null; then echo "USER_ID $USER_ID already exists in the base image" >&2; exit 1; fi \
  && useradd --uid "$USER_ID" --gid "$group_name" --create-home --shell /bin/bash piuser \
  && pi_bin="$(command -v pi)" \
  && mkdir -p /app /work /home/piuser/.pi /home/piuser/.config/pi-web-gateway /home/piuser/.local/bin /home/piuser/.local/share/mise /home/piuser/.config/mise /home/piuser/.cache/mise \
  && printf '#!/usr/bin/env bash\nexec /usr/bin/node %s "$@"\n' "$pi_bin" > /home/piuser/.local/bin/pi \
  && chmod +x /home/piuser/.local/bin/pi \
  && chown -R piuser:"$group_name" /app /work /home/piuser \
  && ln -s /usr/bin/fdfind /usr/local/bin/fd \
  && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /home/piuser/.pi/agent/npm \
  && printf '{\n  "packages": [\n    "npm:@furbyhaxx/pi-session-naming@%s"\n  ]\n}\n' "$SESSION_NAMING_VERSION" > /home/piuser/.pi/agent/settings.json \
  && npm install --prefix /home/piuser/.pi/agent/npm --omit=dev @furbyhaxx/pi-session-naming@${SESSION_NAMING_VERSION} \
  && chown -R piuser:"$(id -gn piuser)" /home/piuser/.pi

WORKDIR /app
COPY Gemfile Gemfile.lock ./
RUN bundle install

COPY . .
RUN chown -R piuser:"$(id -gn piuser)" /app \
  && chmod +x bin/start bin/setup bin/docker-entrypoint bin/gateway-password

USER piuser
ENTRYPOINT ["tini", "--", "/app/bin/docker-entrypoint"]
CMD ["/app/bin/start"]

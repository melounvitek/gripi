# AGENTS.md

## Planning files

This repo may use `TODO.md` for tracking follow-up work, rough ideas, and deferred tasks. When useful, suggest adding items there rather than losing them in chat.

This repo may also have a `PLAN.md`. If present, treat it as the active implementation plan, keep it in mind while working, and avoid drifting from it without discussion. For larger upcoming work, suggest creating or using `PLAN.md`.

When the current plan is completed, move the finished `PLAN.md` into the `plans/` folder.

## Local server

The dev server usually runs as Puma on `100.103.198.74:4567`, logging to `/tmp/pi-web-gateway.log`.

Safe background restart pattern, especially when the user is connected through the web gateway. Always use this detached script; do not manually `kill` the server and then start it in the same foreground tool call, because killing the gateway can disconnect the active user before the replacement process is verified:

```bash
cat > /tmp/restart-pi-web-gateway.sh <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cd /home/vitek/Work/pi-web-gateway
restart_log=/tmp/pi-web-gateway-restart.log
{
  echo "[$(date -Is)] Restart requested"
  old_pids=$(ps -ef | rg 'puma .*pi-web-gateway|rackup|config.ru' | rg -v rg | awk '{print $2}' | tr '\n' ' ' || true)
  if [ -n "${old_pids// }" ]; then
    echo "[$(date -Is)] Stopping old gateway pid(s): $old_pids"
    kill $old_pids || true
    for _ in $(seq 1 20); do
      if ! ps -p $old_pids >/dev/null 2>&1; then
        break
      fi
      sleep 0.2
    done
  else
    echo "[$(date -Is)] No old gateway pid found"
  fi

  echo "[$(date -Is)] Starting gateway"
  nohup mise exec -- bundle exec rackup -o 100.103.198.74 -p 4567 > /tmp/pi-web-gateway.log 2>&1 &
  new_pid=$!
  echo "[$(date -Is)] Started background pid: $new_pid"

  for _ in $(seq 1 30); do
    if ps -p "$new_pid" >/dev/null 2>&1 && curl -fsS --max-time 1 http://100.103.198.74:4567/ >/dev/null 2>&1; then
      echo "[$(date -Is)] Gateway is responding on http://100.103.198.74:4567/"
      exit 0
    fi
    sleep 0.5
  done

  echo "[$(date -Is)] Gateway did not respond in time; process status:"
  ps -p "$new_pid" -f || true
  echo "[$(date -Is)] Recent app log:"
  tail -40 /tmp/pi-web-gateway.log || true
  exit 1
} >> "$restart_log" 2>&1
SH
chmod +x /tmp/restart-pi-web-gateway.sh
nohup /tmp/restart-pi-web-gateway.sh >/tmp/pi-web-gateway-restart-dispatch.log 2>&1 &
```

After dispatching, verify with a separate tool call. If the first verification fails, inspect `/tmp/pi-web-gateway-restart.log` and `/tmp/pi-web-gateway.log`; do not assume the server came back:

```bash
ps -ef | rg 'puma .*pi-web-gateway|rackup|config.ru' | rg -v rg
curl -fsS --max-time 2 -o /dev/null -w '%{http_code}\n' http://100.103.198.74:4567/
tail -80 /tmp/pi-web-gateway-restart.log
```

Do not restart it unless explicitly asked; for code changes, tell the user a restart is needed.

# Example setups

Pi Web Gateway gives browser access to local Pi processes. Do not expose it directly to the public internet.

For remote access, use a private network such as [Tailscale](https://tailscale.com/). It is free for personal use, reliable, and a common default for this kind of setup.

## 1. Local only

Use this when Pi Web Gateway and your browser run on the same machine.

```sh
PI_GATEWAY_HOST=127.0.0.1 mise run start
```

Open <http://localhost:4567>.

This is the simplest and safest setup.

## 2. Gateway server on an always-on computer, app as client

Use this when Pi Web Gateway runs as the server on another computer, and your browser, mobile web app, or desktop app connects as the client.

The server computer can be a spare laptop, desktop, home server, or VPS. A VPS is riskier: only use one if you know how to lock it down at the network level.

Recommended shape:

1. Install Pi Web Gateway and Pi CLI on the server computer.
2. Put the server computer and your client device on the same Tailscale network.
3. Bind the gateway to the server computer's Tailscale address:

   ```sh
   PI_GATEWAY_HOST=100.x.y.z mise run start
   ```

4. Open `http://100.x.y.z:4567` from your browser, mobile web app, or the desktop app's “Add Server…” menu.

Do not bind the gateway to a public interface unless you have added your own strong network-level protection.

### Tailscale HTTPS with a systemd user service

To keep the gateway running after login, create `~/.config/systemd/user/pi-web-gateway.service`:

```ini
[Unit]
Description=Pi Web Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/path/to/pi-web-gateway
EnvironmentFile=-%h/.config/pi-web-gateway/env
Environment=PI_GATEWAY_HOST=127.0.0.1
Environment=PI_GATEWAY_PORT=4567
Environment=PATH=%h/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=%h/.local/bin/mise exec -- bin/start
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

Use the real checkout path for `WorkingDirectory`. The explicit `PATH` is important when `pi` is installed in `~/.local/bin`; systemd user services do not always inherit the same shell environment as your terminal.

Enable and start the gateway:

```sh
systemctl --user daemon-reload
systemctl --user enable --now pi-web-gateway.service
```

Then expose the local gateway over Tailscale HTTPS:

```sh
tailscale serve --bg --yes 4567
```

If Tailscale requires elevated permissions, run this once and retry:

```sh
sudo tailscale set --operator=$USER
```

Useful checks:

```sh
systemctl --user status pi-web-gateway.service --no-pager
journalctl --user -u pi-web-gateway.service -f
tailscale serve status
```

## 3. Local gateway and remote gateway

Use this when you want one gateway server on your laptop and another gateway server on an always-on computer.

Run the local gateway server:

```sh
PI_GATEWAY_HOST=127.0.0.1 mise run start
```

Run the remote gateway server on the always-on computer's Tailscale address:

```sh
PI_GATEWAY_HOST=100.x.y.z mise run start
```

Then choose the gateway you want to use.

You can open either server directly in the browser:

- Local: <http://localhost:4567>
- Remote: `http://100.x.y.z:4567`

Or add both servers to the desktop app and switch between them from the app menu.

Each server runs Pi where that server is installed, with that server's filesystem, repositories, and credentials.

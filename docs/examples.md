# Local and remote setups

Gripi lets a desktop app or web browser use Pi running on the gateway machine. Pi has access to that machine's files, repositories, and credentials, so do not expose Gripi directly to the public internet.

For remote access, use a VPN such as [Tailscale](https://tailscale.com/). It is free for personal use and a common default for this kind of setup.

## Local gateway

Use this when Gripi and your browser or desktop app run on the same machine:

```sh
GRIPI_HOST=127.0.0.1 mise run start
```

Open <http://localhost:4567>. This is the simplest and safest setup.

## Remote gateway over Tailscale

Use this when Pi should run on an always-on desktop, spare laptop, or home server while you connect from another device.

This setup works well even on slower private networks. Gripi is still comfortable to use over Tailscale connections with 100ms+ ping, because most work happens on the gateway machine.

1. Install Gripi and Pi CLI on the gateway machine.
2. Put the gateway machine and client devices on the same Tailscale network.
3. Choose one of the connection options below.

### Direct VPN connection

Bind Gripi to the gateway machine's Tailscale address:

```sh
GRIPI_HOST=100.x.y.z mise run start
```

Open `http://100.x.y.z:4567` in a browser, or add it from the desktop app's **Add Server…** menu.

### HTTPS through Tailscale Serve

Keep Gripi bound to the gateway machine itself:

```sh
GRIPI_HOST=127.0.0.1 mise run start
```

In another terminal, expose it within your Tailscale network over HTTPS:

```sh
tailscale serve --bg --yes 4567
tailscale serve status
```

Open the `https://…ts.net` URL shown by `tailscale serve status`, or add it to the desktop app.

If Tailscale requires elevated permissions, run this once and retry:

```sh
sudo tailscale set --operator=$USER
```

### Optional: keep the gateway running with systemd

Create `~/.config/systemd/user/gripi.service` on the gateway machine:

```ini
[Unit]
Description=Gripi
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/path/to/gripi
EnvironmentFile=-%h/.config/gripi/env
Environment=GRIPI_HOST=127.0.0.1
Environment=GRIPI_PORT=4567
Environment=PATH=%h/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/absolute/path/to/mise exec -- bin/start
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

Replace `WorkingDirectory` with the Gripi checkout and `ExecStart` with the path reported by `command -v mise`. The unit above is configured for Tailscale Serve; for a direct VPN connection, replace `127.0.0.1` with the gateway machine's Tailscale address.

The explicit `PATH` includes common Pi installation locations. If `command -v pi` reports another directory, add that directory to `PATH` or configure the [custom Pi runtime](configuration.md#custom-pi-runtime).

Enable the service:

```sh
systemctl --user daemon-reload
systemctl --user enable --now gripi.service
```

A user service normally starts after login. To keep it running after logout and start it at boot, enable lingering for that user:

```sh
sudo loginctl enable-linger "$USER"
```

Useful checks:

```sh
systemctl --user status gripi.service --no-pager
journalctl --user -u gripi.service -f
tailscale serve status
```

## Multiple gateways

The desktop app can store a local gateway and one or more remote gateways and switch between them. Pi always runs on the selected gateway machine with access to that machine's filesystem, repositories, and credentials.

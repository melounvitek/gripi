# Local and remote setups

Pi runs on the gateway machine with access to its files, repositories, and credentials, so never expose the gateway directly to the public internet. For remote access, use a private network such as [Tailscale](https://tailscale.com/), which is free for personal use, and keep [access approval](configuration.md#access-approval) enabled.

These examples assume the default installation in `~/.local/share/gripi`. In a development checkout, `mise run start` does the same as `bin/start`.

## Local gateway

Use this when the gateway and your browser or desktop app run on the same machine:

```sh
~/.local/share/gripi/bin/start
```

Open <http://localhost:4567>. This is the simplest and safest setup, because the gateway listens only on `127.0.0.1`.

## Remote gateway over Tailscale

Use this to run Pi on an always-on desktop, spare laptop, or home server and connect from other devices. It works well even on slow connections, because the work happens on the gateway machine.

1. Install Gripi and Pi CLI on the gateway machine.
2. Add the gateway machine and your other devices to the same Tailscale network.
3. Connect with HTTPS through Tailscale Serve (recommended) or directly over the VPN.

### HTTPS through Tailscale Serve

This is the recommended setup, because browser notifications, including Web Push on iPhone, require HTTPS.

Publish port 4567 to your Tailscale network over HTTPS:

```sh
tailscale serve --bg --yes 4567
```

The command prints the machine’s `https://….ts.net` URL; `tailscale serve status` shows it again. Add its hostname to `~/.config/gripi/env`:

```sh
GRIPI_PERMITTED_HOSTS=gateway.example.ts.net
GRIPI_TRUST_PROXY_HEADERS=1
```

Start Gripi on localhost as usual:

```sh
~/.local/share/gripi/bin/start
```

Open the `https://….ts.net` URL, or add it in the desktop app with **File → Add Server…**. Do not enable `GRIPI_TRUST_PROXY_HEADERS` if clients can bypass Tailscale Serve and connect to the gateway directly.

If Tailscale requires elevated permissions, run this once and try again:

```sh
sudo tailscale set --operator=$USER
```

### Direct VPN connection

Gripi can also listen on the machine’s Tailscale address. Tailscale encrypts the traffic, but Gripi receives plain HTTP and cannot tell it apart from an unencrypted LAN connection, so allow it in `~/.config/gripi/env`:

```sh
GRIPI_ALLOW_INSECURE_REMOTE_HTTP=1
```

Then start Gripi on the Tailscale address:

```sh
GRIPI_HOST=100.x.y.z ~/.local/share/gripi/bin/start
```

Open `http://100.x.y.z:4567`, or add it in the desktop app. Only use this setting on an encrypted, access-controlled network such as Tailscale, never for ordinary LAN or Wi-Fi access. Browsers do not treat this URL as secure, so notifications do not work.

## Keep the gateway running with systemd

On a Linux gateway, create `~/.config/systemd/user/gripi.service` as the OS user whose Pi is installed, authenticated, and configured:

```ini
[Unit]
Description=Gripi
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%h/.local/share/gripi
EnvironmentFile=-%h/.config/gripi/env
Environment=GRIPI_HOST=127.0.0.1
Environment=GRIPI_PORT=4567
Environment=PATH=%h/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=%h/.local/bin/mise exec -- bin/start
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
```

If `command -v mise` reports a different path, use it in `ExecStart`. This unit suits Tailscale Serve; for a direct VPN connection, set `GRIPI_HOST` to the machine’s Tailscale address.

`EnvironmentFile` passes the settings in `~/.config/gripi/env` to Pi as well, except the admin password, and they override the `Environment=` lines. systemd does not load your shell profile, so credentials and variables set only there do not reach Pi. If `command -v pi` reports a directory that is not in the `PATH` above, add it, or set a [custom Pi runtime](configuration.md#custom-pi-runtime).

Enable the service:

```sh
systemctl --user daemon-reload
systemctl --user enable --now gripi.service
```

A user service normally starts only after you log in. To keep it running after logout and start it at boot, enable lingering:

```sh
sudo loginctl enable-linger "$USER"
```

Useful checks:

```sh
systemctl --user status gripi.service --no-pager
journalctl --user -u gripi.service -f
tailscale serve status
```

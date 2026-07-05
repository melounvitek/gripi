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

## 2. VPS server, app as client

Use this when Pi Web Gateway runs on a VPS and you connect from the desktop or mobile app.

Recommended shape:

1. Install Pi Web Gateway and Pi CLI on the VPS.
2. Put the VPS and your client device on the same Tailscale network.
3. Bind the gateway to the VPS Tailscale address:

   ```sh
   PI_GATEWAY_HOST=100.x.y.z mise run start
   ```

4. Open `http://100.x.y.z:4567` from your browser, mobile web app, or the desktop app's “Add Server…” menu.

Do not bind the gateway to a public interface unless you have added your own strong network-level protection.

## 3. Local and VPS

Use this when you want local sessions on your laptop and separate sessions on a VPS.

Run one gateway locally:

```sh
PI_GATEWAY_HOST=127.0.0.1 mise run start
```

Run another gateway on the VPS Tailscale address:

```sh
PI_GATEWAY_HOST=100.x.y.z mise run start
```

Then connect to whichever gateway you want to use:

- Local: <http://localhost:4567>
- VPS: `http://100.x.y.z:4567`

Each server runs Pi where that server is installed, with that server's filesystem, repositories, and credentials.

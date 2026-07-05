# TODO

## Electron desktop shell

- [x] Check small-window browser-like behavior.
  - [x] The shell should keep tabs, setup forms, offline panels, and action buttons usable when the app window is made narrow or short.
  - [x] The gateway web UI remains responsible for its own responsive layout inside each tab.

- [ ] Add a custom app logo.
  - [ ] The packaged app currently uses the default Electron icon.
  - [ ] Configure electron-builder icons for macOS and Linux.

- [ ] Verify offline behavior before release.
  - [ ] Default `http://localhost:4567/` when the server is not running.
  - [ ] DNS failure for a remote/private gateway URL.
  - [ ] Connection refused for an otherwise valid host.
  - [ ] Saving a corrected URL from the offline panel.
  - [ ] Retrying without saving.
  - [ ] Switching tabs after one gateway is offline.

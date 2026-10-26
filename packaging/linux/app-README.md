# Almagest desktop client package

This package contains the Almagest desktop client. It does not contain or
install the `almagestd` daemon.

The client connects to `/run/almagest/admin.sock` by default. Install and start
the separate `almagest` package to use this endpoint. The user that runs the
client must have access to the `almagest` group.

Package contents:

- `almagest-app`: desktop client (archive), installed as `/usr/bin/almagest-app`
  by system packages.
- `share/applications/almagest-app.desktop`: desktop menu entry.
- `etc/xdg/autostart/almagest-app.desktop`: starts the client in the system tray
  when the user signs in. A user can disable it with the desktop environment's
  startup application settings.
- `share/icons/hicolor/128x128/apps/almagest.png`: desktop icon.
- `share/licenses/almagest-app/LICENSE`: GPL license text.

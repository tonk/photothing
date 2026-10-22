#!/bin/sh
# After removal: let systemd forget the units. The configuration in /etc/photothing and the data in
# /var/lib/photothing (database, key, backups) and the service user are deliberately left alone.
set -e
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

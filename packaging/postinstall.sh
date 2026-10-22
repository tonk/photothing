#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# After the files are unpacked: let systemd see the new units, and restart what is already running so
# an upgrade takes effect. Nothing is enabled or started on a first install: it needs configuring first.
set -e
if [ -d /run/systemd/system ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl try-restart photothing.service 'photothing@*.service' >/dev/null 2>&1 || true
fi

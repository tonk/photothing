#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Before the files are unpacked: make sure the service user and group exist.
set -e
if ! getent passwd photothing >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir /var/lib/photothing \
        --shell /usr/sbin/nologin photothing
fi

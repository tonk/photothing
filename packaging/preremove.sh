#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Before removal (not on upgrade): stop the services. $1 is "remove" for deb and "0" for rpm.
set -e
case "$1" in
    remove|0)
        if [ -d /run/systemd/system ]; then
            systemctl disable --now photothing.service 'photothing@*.service' >/dev/null 2>&1 || true
        fi
        ;;
esac

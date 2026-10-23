#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Copyright (C) 2026 Ton Kersten
# Installs ffmpeg, libheif's command line tools and exiftool on a CI runner, so that the tests that need them
# run. The runner's package mirror is sometimes very slow (ffmpeg pulls in about ninety packages, and a run once
# spent 25 minutes just downloading them), so:
#   1. the packages are first only downloaded, in attempts that are cut short after ATTEMPT_TIMEOUT seconds:
#      apt keeps what it already has, so every attempt carries on where the last stopped, and stopping a
#      download is harmless;
#   2. then they are installed from that cache, which takes seconds and is never interrupted.
# For tests: APT_GET and SUDO replace the commands, ATTEMPTS and ATTEMPT_TIMEOUT the limits.
set -u

apt="${APT_GET:-apt-get}"
sudo="${SUDO-sudo}"
attempts="${ATTEMPTS:-5}"
limit="${ATTEMPT_TIMEOUT:-300}"
pkgs=(ffmpeg libheif-examples libimage-exiftool-perl)
opts=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
export DEBIAN_FRONTEND=noninteractive

ok=0
for attempt in $(seq 1 "$attempts"); do
  echo "download attempt $attempt of $attempts (at most ${limit}s)"
  if $sudo timeout "$limit" "$apt" "${opts[@]}" update &&
     $sudo timeout "$limit" "$apt" "${opts[@]}" install --download-only -y --no-install-recommends "${pkgs[@]}"; then
    ok=1
    break
  fi
  echo "::warning::attempt $attempt did not finish in time (a slow package mirror?); apt keeps what it has downloaded"
done
if [ "$ok" != 1 ]; then
  echo "::error::could not download the packages in $attempts attempts"
  exit 1
fi
$sudo "$apt" "${opts[@]}" install -y --no-install-recommends "${pkgs[@]}"

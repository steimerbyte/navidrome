#!/usr/bin/env bash
# Combined container: ytptube (downloader) and Navidrome in one image.
# Navidrome runs in the foreground for UI and API; ytptube listens on loopback and
# is reached only through Navidrome's /downloader proxy (YTP_HOST=127.0.0.1).
#
# ytptube's /entrypoint.sh forwards its arguments to start-services and exits with
# "No YTPTube command was provided" when given none, so the command it would get
# from its own image CMD is passed explicitly.
set -euo pipefail

YTP_CMD=(/opt/python/bin/python /app/app/main.py --ytp-process)

mkdir -p /data /music "${YTP_CONFIG_PATH:-/config}" "${YTP_DOWNLOAD_PATH:-/downloads}" \
  "${YTP_TEMP_PATH:-/tmp}"

/entrypoint.sh "${YTP_CMD[@]}" &
ytp_pid=$!

/app/navidrome &
nd_pid=$!

stop() {
  trap - TERM INT
  kill -TERM "$ytp_pid" "$nd_pid" 2>/dev/null || true
  wait "$ytp_pid" "$nd_pid" 2>/dev/null || true
}
trap stop TERM INT

status=0
# One service dying leaves the container half-working: stop everything instead.
wait -n "$ytp_pid" "$nd_pid" || status=$?
echo "combined entrypoint: one service exited (status $status), stopping the container"
stop
exit "$status"

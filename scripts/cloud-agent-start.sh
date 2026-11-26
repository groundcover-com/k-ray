#!/usr/bin/env bash
# Cursor Cloud Agent start step (.cursor/environment.json). The VM has no
# systemd, so nothing starts dockerd on boot; launch it and return once the
# API answers. Idempotent: a healthy daemon is left alone.
set -euo pipefail

if sudo docker info >/dev/null 2>&1; then
	echo "dockerd already running: $(sudo docker version --format '{{.Server.Version}}')"
	exit 0
fi

stop_dockerd() {
	# Only dockerd. Its managed containerd exits with it. A host-wide
	# `pkill containerd` would also take down a Kind node's runtime if
	# this script is re-run by hand mid-session.
	sudo pkill -TERM -x dockerd || true
	local _
	for _ in $(seq 1 20); do
		pgrep -x dockerd >/dev/null || return 0
		sleep 0.5
	done
	sudo pkill -KILL -x dockerd || true
	sleep 1
}

if pgrep -x dockerd >/dev/null; then
	echo "dockerd process exists but is not serving; restarting" >&2
	stop_dockerd
fi
sudo rm -f /var/run/docker.pid /var/run/docker.sock
sudo sh -c 'nohup dockerd >>/var/log/dockerd.log 2>&1 &'

for _ in $(seq 1 60); do
	if sudo docker info >/dev/null 2>&1; then
		echo "dockerd ready: $(sudo docker version --format '{{.Server.Version}}')"
		exit 0
	fi
	sleep 1
done

echo "dockerd did not become ready" >&2
sudo tail -50 /var/log/dockerd.log >&2 || true
exit 1

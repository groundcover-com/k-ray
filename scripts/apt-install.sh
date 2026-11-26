#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
	echo "usage: apt-install.sh PACKAGE..." >&2
	exit 2
fi

readonly max_attempts=5

for ((attempt = 1; attempt <= max_attempts; attempt++)); do
	if sudo apt-get \
		-o Acquire::Retries=5 \
		-o Acquire::http::Timeout=20 \
		-o Acquire::https::Timeout=20 \
		update &&
		sudo DEBIAN_FRONTEND=noninteractive apt-get \
			-o Acquire::Retries=5 \
			-o Acquire::http::Timeout=20 \
			-o Acquire::https::Timeout=20 \
			install -y "$@"; then
		exit 0
	fi

	if ((attempt == max_attempts)); then
		echo "apt transaction failed after ${max_attempts} attempts" >&2
		exit 1
	fi

	delay=$((attempt * 10))
	echo "apt transaction failed (attempt ${attempt}/${max_attempts}); retrying in ${delay}s" >&2
	sleep "$delay"
done

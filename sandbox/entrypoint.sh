#!/bin/bash
# Sandbox entrypoint. Starts dockerd in the background then becomes a
# long-running process so Daytona keeps the sandbox alive.
#
# dockerd needs --privileged (or equivalent capabilities) on the sandbox.
# If the daemon can't start (e.g. missing privileges), we log and continue
# rather than failing the whole sandbox — non-Docker workflows still work.
set -uo pipefail

if ! pgrep -x dockerd >/dev/null 2>&1; then
    sudo -n bash -c '
        dockerd \
            --host=unix:///var/run/docker.sock \
            --host=tcp://127.0.0.1:2375 \
            > /var/log/dockerd.log 2>&1 &
    '

    for _ in $(seq 1 60); do
        if [ -S /var/run/docker.sock ] && docker info >/dev/null 2>&1; then
            break
        fi
        sleep 0.5
    done

    if [ -S /var/run/docker.sock ]; then
        sudo -n chgrp docker /var/run/docker.sock 2>/dev/null || true
        sudo -n chmod 660 /var/run/docker.sock 2>/dev/null || true
    fi
fi

exec sleep infinity

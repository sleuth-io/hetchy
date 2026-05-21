#!/bin/bash
# Sandbox entrypoint. Starts dockerd in the background then becomes a
# long-running process so Daytona keeps the sandbox alive.
#
# Daytona sandboxes don't run --privileged, and overlay/fuse-overlayfs has
# been unreliable in Daytona Cloud for nested Docker workloads. Use Docker's
# vfs storage driver: it is slower but only depends on ordinary filesystem
# operations, and it is the path we have verified with `docker run hello-world`
# inside a Daytona Cloud sandbox.
#
# We considered disabling iptables / bridge networking to sidestep
# the "nf_tables permission denied" failure dockerd hits when run
# nested inside a non-CAP_NET_ADMIN container (e.g. Docker Desktop).
# Daytona's actual sandbox runtime grants those caps, so the default
# bridge driver works there — the deeper "exec invalid argument"
# failure inner containers hit on Daytona is a kernel/AppArmor
# restriction we can't change from the daemon side. Disabling bridge
# would have no upside in Daytona and would break the default
# network compose files rely on, so we leave dockerd's networking at
# its defaults and rely on the deferred-capability path when an
# individual repo can't get Docker to do useful work.
set -uo pipefail

docker_ready() {
    [ -S /var/run/docker.sock ] && docker info >/dev/null 2>&1
}

if ! docker_ready; then
    sudo -n install -d -m 0755 /etc/docker

    # A stopped/resumed sandbox can preserve /var/run/docker.pid and
    # /var/run/docker.sock even though dockerd is gone. Docker refuses to
    # start when the stale pid file references any live process, even if that
    # process is not dockerd, so readiness is `docker info`, not pgrep.
    if pgrep -x dockerd >/dev/null 2>&1; then
        sudo -n pkill -TERM -x dockerd 2>/dev/null || true
        for _ in $(seq 1 40); do
            pgrep -x dockerd >/dev/null 2>&1 || break
            sleep 0.25
        done
        sudo -n pkill -KILL -x dockerd 2>/dev/null || true
    fi
    sudo -n rm -f /var/run/docker.pid /var/run/docker.sock

    sudo -n tee /etc/docker/daemon.json >/dev/null <<JSON
{
  "features": { "containerd-snapshotter": false },
  "storage-driver": "vfs"
}
JSON

    sudo -n bash -c '
        dockerd \
            --host=unix:///var/run/docker.sock \
            --host=tcp://127.0.0.1:2375 \
            > /var/log/dockerd.log 2>&1 &
    '

    for _ in $(seq 1 80); do
        if docker_ready; then
            break
        fi
        sleep 0.5
    done

    if [ -S /var/run/docker.sock ]; then
        sudo -n chgrp docker /var/run/docker.sock 2>/dev/null || true
        sudo -n chmod 660 /var/run/docker.sock 2>/dev/null || true
    fi

    if ! docker_ready; then
        echo "[hetchy-entrypoint] WARNING: dockerd did not become ready; see /var/log/dockerd.log" >&2
    fi
fi

exec sleep infinity

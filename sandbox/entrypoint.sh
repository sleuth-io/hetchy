#!/bin/bash
# Sandbox entrypoint. Starts dockerd in the background then becomes a
# long-running process so Daytona keeps the sandbox alive.
#
# Daytona sandboxes don't run --privileged, which means kernel-level
# overlay mounts (Docker's default container filesystem) return -EINVAL.
# We pre-write a /etc/docker/daemon.json that:
#   - disables Docker 29's containerd-snapshotter mode (the snapshotter
#     ignores the storage-driver knob and still tries kernel overlayfs)
#   - selects fuse-overlayfs as the storage driver (userspace overlay,
#     works without CAP_SYS_ADMIN as long as /dev/fuse is exposed)
# If fuse-overlayfs also fails — e.g. /dev/fuse isn't available — dockerd
# still logs to /var/log/dockerd.log and the sandbox stays usable for
# non-Docker workflows; Docker-dependent repos surface a clear error
# at the bootstrap setup.sh layer instead of at sandbox-start time.
set -uo pipefail

if ! pgrep -x dockerd >/dev/null 2>&1; then
    sudo -n install -d -m 0755 /etc/docker

    # Probe what storage driver we can actually run. fuse-overlayfs needs
    # /dev/fuse plus a kernel that has the fuse module available; the
    # vfs driver works anywhere (slower, but no syscalls beyond the
    # ordinary filesystem). Fall back to vfs if the probe fails so a
    # sandbox without working fuse still has a usable Docker daemon.
    storage_driver=vfs
    if [ -c /dev/fuse ] && command -v fuse-overlayfs >/dev/null 2>&1; then
        probe_dir=$(mktemp -d -t fuse-probe-XXXX)
        mkdir -p "${probe_dir}"/{lower,upper,work,merged}
        if fuse-overlayfs \
                -o "lowerdir=${probe_dir}/lower,upperdir=${probe_dir}/upper,workdir=${probe_dir}/work" \
                "${probe_dir}/merged" >/dev/null 2>&1; then
            fusermount -u "${probe_dir}/merged" 2>/dev/null || true
            storage_driver=fuse-overlayfs
        fi
        rm -rf "${probe_dir}" 2>/dev/null || true
    fi

    sudo -n tee /etc/docker/daemon.json >/dev/null <<JSON
{
  "features": { "containerd-snapshotter": false },
  "storage-driver": "${storage_driver}"
}
JSON

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

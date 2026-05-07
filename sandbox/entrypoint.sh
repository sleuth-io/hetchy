#!/bin/bash
# Sandbox entrypoint. Runs the docker-in-docker setup steps that the
# upstream docker:dind image's entrypoint does (we use a Debian-based
# Daytona base instead of docker:dind so we can keep the polyglot
# toolchain — Chrome, sury PHP, pgdg postgres, gh CLI — that doesn't
# port cleanly to Alpine), then starts dockerd in the background and
# becomes a long-running process so Daytona keeps the sandbox alive.
#
# The setup steps mirror moby/moby's hack/dind plus the iptables-legacy
# fallback from docker-library/docker's dockerd-entrypoint.sh:
#
#   - container=docker tells containerd's apparmor module it's nested,
#     which avoids it trying to apply a host-side profile that doesn't
#     exist inside the sandbox (the symptom is `runc exec: invalid
#     argument` when the inner daemon tries to start a container).
#   - securityfs at /sys/kernel/security is required for AppArmor to
#     report status; without it nested AppArmor calls fail.
#   - cgroup v2 nesting: dockerd needs to write the controller list
#     to cgroup.subtree_control, which is rejected (EBUSY) if any
#     processes — including PID 1 — are still in the root cgroup.
#     We move them to /sys/fs/cgroup/init in a retry loop because
#     "docker exec" into the outer sandbox spawns new root-cgroup
#     procs that race the subtree_control write.
#   - mount --make-rshared / makes the rootfs propagate mount events
#     so dockerd's overlay/tmpfs mounts don't get hidden from inner
#     containers.
#   - iptables-legacy fallback: if nf_tables can't be modprobed (host
#     kernel module not loadable from inside the sandbox), fall back
#     to the legacy iptables binaries via Debian's update-alternatives.
#
# The fuse-overlayfs / vfs storage-driver probe is unchanged: even on
# a working dind setup, our sandbox doesn't get the kernel overlayfs
# capability dockerd's default driver wants, so we still need to pick
# a userspace alternative.
set -uo pipefail

# Tell containerd's apparmor module we're nested. Read by
# containerd/pkg/apparmor/apparmor_linux.go — without this, the inner
# daemon tries to apply a host AppArmor profile that doesn't exist
# in the sandbox.
export container=docker

if ! pgrep -x dockerd >/dev/null 2>&1; then
    sudo -n install -d -m 0755 /etc/docker

    # ------------------------------------------------------------------
    # Pre-dockerd kernel/cgroup setup. All needs root, batched into one
    # sudo call so we don't pay sudo's PAM cost five times.
    # ------------------------------------------------------------------
    sudo -n bash <<'SETUP'
set -uo pipefail

# Mount securityfs so AppArmor status calls work from inside the sandbox.
if [ -d /sys/kernel/security ] && ! mountpoint -q /sys/kernel/security; then
    mount -t securityfs none /sys/kernel/security 2>/dev/null || true
fi

# cgroup v2 nesting: move all root-cgroup procs into /sys/fs/cgroup/init
# and enable the full controller set on the root subtree. Without this,
# dockerd's attempt to create child cgroups for containers fails with
# EBUSY and the inner runc exits "invalid argument".
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    mkdir -p /sys/fs/cgroup/init
    # Loop because outer "docker exec" can race us by spawning new
    # root-cgroup procs between the move and the subtree_control write.
    # Cap the retries so a permanently-broken cgroup doesn't wedge boot.
    for _ in $(seq 1 20); do
        if xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null \
            && sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
                > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null; then
            break
        fi
        sleep 0.1
    done
fi

# Make mount propagation rshared so dockerd's overlay/tmpfs mounts are
# visible to inner containers.
mount --make-rshared / 2>/dev/null || true

# iptables backend selection. dockerd needs working iptables; if
# nf_tables can't be modprobed (host doesn't expose the module to the
# sandbox) fall back to legacy via Debian's update-alternatives. The
# `iptables -nL` probe runs against whatever alternative is currently
# active.
modprobe nf_tables 2>/dev/null || true
if ! iptables -nL >/dev/null 2>&1; then
    modprobe ip_tables 2>/dev/null || true
    if command -v iptables-legacy >/dev/null 2>&1; then
        update-alternatives --set iptables /usr/sbin/iptables-legacy >/dev/null 2>&1 || true
        update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy >/dev/null 2>&1 || true
    fi
fi
SETUP

    # ------------------------------------------------------------------
    # Storage-driver probe. Daytona doesn't grant the kernel-level
    # capabilities Docker's default overlayfs needs, so we pick
    # fuse-overlayfs when /dev/fuse is exposed and fall back to vfs
    # otherwise.
    # ------------------------------------------------------------------
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
        export container=docker
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

#!/bin/bash
# Sandbox entrypoint. Brings dockerd up before becoming a long-running
# pid 1 so the agent can use Docker for downstream work (compose-up'ing
# Postgres, redis, etc.). Daytona sandboxes don't run --privileged, so
# we layer two strategies and pick whichever the host actually allows:
#
#   1. Rootful dockerd with iptables / bridge networking DISABLED.
#      Daytona doesn't grant CAP_NET_ADMIN, so the default bridge
#      driver fails at "create NAT chain DOCKER: nf_tables permission
#      denied". Telling dockerd not to manage iptables and not to
#      configure a bridge sidesteps the kernel ACL entirely. Containers
#      run with --network host (port-publishing inherits the sandbox's
#      port; outbound traffic uses the sandbox's interface). For most
#      hetchy use cases — postgres on a port, redis on a port — this
#      is exactly what the agent wants. Storage uses fuse-overlayfs
#      (we ship the userspace driver) when /dev/fuse is available;
#      vfs otherwise. Layer extraction works because fuse-overlayfs
#      doesn't need CAP_SYS_ADMIN to do its job.
#
#   2. Rootless dockerd-rootless.sh as the daytona user. Used as a
#      fallback when (1) can't even be reached because the sandbox is
#      so locked down that even fuseless dockerd as root fails to
#      start. Rootless avoids every privileged-capability ask: it
#      uses user namespaces, slirp4netns for networking, and writes
#      its socket at $XDG_RUNTIME_DIR/docker.sock. The daytona user's
#      shell already sets DOCKER_HOST to that socket via .bashrc so
#      `docker` calls route there transparently.
#
# If both strategies fail dockerd is just unavailable; the sandbox
# still works for non-Docker workflows. Bootstrap surfaces "docker
# unreachable" via a deferred capability the agent records, instead
# of the sandbox refusing to start.
set -uo pipefail

# Helper: poll for the rootful socket. Returns 0 if dockerd is up.
wait_for_rootful_docker() {
    for _ in $(seq 1 60); do
        if [ -S /var/run/docker.sock ] && docker info >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}

# Strategy 1 — rootful dockerd.
start_rootful_docker() {
    pgrep -x dockerd >/dev/null 2>&1 && return 0

    sudo -n install -d -m 0755 /etc/docker || return 1

    # Probe for fuse-overlayfs. /dev/fuse must be present and the
    # userspace driver installed. Daytona does expose /dev/fuse so
    # the probe usually selects fuse-overlayfs; vfs is the universal
    # fallback (slow but works without privileges).
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

    # iptables/ip6tables/bridge=none are the load-bearing flags. The
    # default bridge driver tries to install nft NAT rules at startup;
    # in a non-CAP_NET_ADMIN sandbox that fails with "Could not fetch
    # rule set generation id: Permission denied" and dockerd refuses
    # to start. Disabling all three lets dockerd come up; containers
    # then run with --network host (the only network they get) which
    # is fine for the "publish a port for compose" use case the
    # bootstrap loop needs.
    sudo -n tee /etc/docker/daemon.json >/dev/null <<JSON
{
  "features": { "containerd-snapshotter": false },
  "storage-driver": "${storage_driver}",
  "iptables": false,
  "ip6tables": false,
  "bridge": "none"
}
JSON

    sudo -n bash -c '
        dockerd \
            --host=unix:///var/run/docker.sock \
            --host=tcp://127.0.0.1:2375 \
            > /var/log/dockerd.log 2>&1 &
    '

    if ! wait_for_rootful_docker; then
        return 1
    fi

    sudo -n chgrp docker /var/run/docker.sock 2>/dev/null || true
    sudo -n chmod 660 /var/run/docker.sock 2>/dev/null || true
    return 0
}

# Strategy 2 — rootless dockerd. Runs as the daytona user; uses
# slirp4netns for the inner network so no host-level capabilities are
# needed. The socket lands at $XDG_RUNTIME_DIR/docker.sock and the
# daytona user's .bashrc already exports DOCKER_HOST pointing there.
start_rootless_docker() {
    # newuidmap / newgidmap need the right entries in /etc/subuid +
    # /etc/subgid for uid 1000. The packages we install set them up,
    # but defensively check before launching.
    if ! getent passwd daytona >/dev/null; then return 1; fi

    # Run as daytona via sudo -u, but pass the env explicitly because
    # `sudo` strips most of it. Capture rootlesskit's log so a failure
    # is debuggable from the sandbox shell.
    sudo -n -u daytona env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        PATH=/usr/sbin:/usr/bin:/bin \
        nohup dockerd-rootless.sh \
            > /var/log/dockerd-rootless.log 2>&1 < /dev/null &

    for _ in $(seq 1 60); do
        if [ -S /run/user/1000/docker.sock ]; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}

if ! start_rootful_docker; then
    echo "[entrypoint] rootful dockerd unavailable, falling back to rootless" >&2
    if ! start_rootless_docker; then
        echo "[entrypoint] rootless dockerd also failed; docker will be unavailable" >&2
    fi
fi

exec sleep infinity

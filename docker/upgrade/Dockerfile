ARG UBUNTU_VERSION=24.04
FROM ubuntu:${UBUNTU_VERSION}
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv dbus curl jq ca-certificates lz4 python3 procps iproute2 sudo && rm -rf /var/lib/apt/lists/*
RUN ln -s /dev/null /etc/systemd/system/systemd-binfmt.service && ln -s /dev/null /etc/systemd/system/proc-sys-fs-binfmt_misc.automount
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]

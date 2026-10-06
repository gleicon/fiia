# Fiia E2E (systemd): a systemd-capable target so the agent role's systemd
# unit, sd_notify readiness, watchdog, and service-state drift can be exercised
# for real. Multi-arch (unlike jrei/systemd-ubuntu, which is amd64-only) —
# builds natively on arm64 Colima and x86_64 CI.
FROM ubuntu:24.04
ENV container=docker
RUN apt-get update -q && apt-get install -y -q --no-install-recommends \
      systemd systemd-sysv dbus python3 \
  && rm -f /lib/systemd/system/systemd-update-utmp-runlevel.service \
  && systemctl enable dbus.socket dbus.service >/dev/null 2>&1 \
  # Minimal fstab: the base image's boot/UEFI mounts never appear in a
  # container and stall local-fs.target -> is-system-running stays in
  # "initializing" forever.
  && printf 'none /proc proc defaults 0 0\nnone /dev/pts devpts defaults 0 0\n' > /etc/fstab \
  # Disable the GPT auto-mount generator: the privileged container sees the
  # host's disks, and systemd auto-creates boot.mount/boot-efi.mount for them,
  # which never settle and block the default target.
  && ln -sf /dev/null /usr/lib/systemd/system-generators/systemd-gpt-auto-generator \
  # Mask units that are irrelevant (or fail) in a container: systemd-logind
  # races dbus at boot, and disk scrub/utmp/pcrphase units reference host disks.
  && systemctl mask systemd-logind.service \
       getty@.service serial-getty@.service console-getty.service \
       systemd-firstboot.service systemd-update-utmp.service \
       e2scrub_reap.service systemd-pcrphase.service \
     >/dev/null 2>&1 \
  && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
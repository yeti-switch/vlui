#!/bin/sh
set -e

PREFIX=/opt/vlui

# No user is created here. The unit runs with DynamicUser=yes, so systemd
# allocates a transient UID for the lifetime of the service and reclaims it
# afterwards — nothing to add, own, or clean up on removal.

# The package ships config.example.yml only. The real config.yml is written by
# whoever deploys (it holds the OIDC client secret and the cookie signing key),
# so the package never writes it and can never clobber it on upgrade.
#
# It is handed to the transient user by systemd's LoadCredential=, which reads it
# as root — so it can stay root-only.
if [ -f "$PREFIX/etc/config.yml" ]; then
    chown root:root "$PREFIX/etc/config.yml"
    chmod 0600 "$PREFIX/etc/config.yml"
fi
chmod 0750 "$PREFIX/etc"

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload

    # Restart only if it was already running: a fresh install has no config.yml
    # yet, and starting it would just log a failure. Enable it once the config
    # is in place:
    #
    #   systemctl enable --now vlui
    if systemctl is-active --quiet vlui; then
        systemctl restart vlui
    fi
fi

exit 0

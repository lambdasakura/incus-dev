#!/bin/sh
# Create the account 'idev shell' lands in, holding the ids workspace.owner
# maps onto. Written so it can be re-run.
set -eu

user="${DEV_USER:-developer}"
uid="${DEV_UID:-1000}"
gid="${DEV_GID:-$uid}"

# The ids have to match workspace.owner exactly: that mapping is what makes
# files this account writes belong to whoever ran idev.
if ! getent group "$gid" >/dev/null; then
    groupadd --gid "$gid" "$user"
fi
group="$(getent group "$gid" | cut -d: -f1)"

if ! id "$user" >/dev/null 2>&1; then
    # --non-unique: the image usually ships an account on uid 1000 already
    # (Ubuntu's is "ubuntu"). Both names then exist; what the workspace cares
    # about is the id.
    useradd --create-home --shell /bin/bash --non-unique \
        --uid "$uid" --gid "$group" "$user"
fi

echo "$user ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$user"
chmod 0440 "/etc/sudoers.d/90-$user"

chown "$uid:$gid" "/home/$user"

# Nothing is written into /workspace here. This runs as root, and under
# raw+owner root is not the host user, so a file created here would belong to
# a subuid on the host.

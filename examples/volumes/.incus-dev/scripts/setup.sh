#!/bin/sh
# Make the volumes usable by the account 'idev shell' lands in.
# Written so it can be re-run: 'idev provision' is expected to be idempotent.
set -eu

user="${DEV_USER:-developer}"
uid="${DEV_UID:-1000}"
gid="${DEV_GID:-$uid}"
home="/home/$user"

if ! getent group "$gid" >/dev/null; then
    groupadd --gid "$gid" "$user"
fi
group="$(getent group "$gid" | cut -d: -f1)"

if ! id "$user" >/dev/null 2>&1; then
    # The home directory already exists whenever a volume is mounted below it,
    # because the mount is made before provisioning runs. useradd then says it
    # is not copying /etc/skel, which is expected, and leaves the directory
    # owned by root -- see the chown below.
    useradd --create-home --shell /bin/bash --non-unique \
        --uid "$uid" --gid "$group" "$user"
fi
echo "$user ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$user"
chmod 0440 "/etc/sudoers.d/90-$user"

# The home directory itself. Not -R: the tree below holds a mounted volume,
# and walking a populated module cache on every provision costs more than it
# is worth.
chown "$uid:$gid" "$home"

# The volume mount points, for the same reason: a volume arrives empty and
# owned by root, so an account that is not root cannot write to it until
# something says otherwise. These are not host directories, so nothing outside
# the container is touched.
for dir in "$home/go/pkg/mod" /var/cache/apt/archives; do
    mkdir -p "$dir"
    chown "$uid:$gid" "$dir"
done

# postgres keeps its own account; give it its directory rather than the
# developer.
if id postgres >/dev/null 2>&1; then
    mkdir -p /var/lib/postgresql/data
    chown postgres:postgres /var/lib/postgresql/data
    chmod 0700 /var/lib/postgresql/data
fi

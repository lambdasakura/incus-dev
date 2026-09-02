#!/bin/sh
# Create the account 'idev shell' lands in. Written so it can be re-run.
set -eu

user="${DEV_USER:-developer}"
uid="${DEV_UID:-1000}"
gid="${DEV_GID:-$uid}"

# The group first: under idmap: shift the group of a file the container writes
# is the host group of the same gid, so it has to be yours too.
if ! getent group "$gid" >/dev/null; then
    groupadd --gid "$gid" "$user"
fi
group="$(getent group "$gid" | cut -d: -f1)"

if ! id "$user" >/dev/null 2>&1; then
    # --non-unique: the uid asked for is the host's, and the image usually
    # ships an account holding it already (Ubuntu's images ship "ubuntu" with
    # uid 1000). Both names then exist and 'id' reports whichever getent
    # returns first, which is cosmetic: what matters for the workspace is the
    # uid, and it is the one asked for.
    useradd --create-home --shell /bin/bash --non-unique \
        --uid "$uid" --gid "$group" "$user"
fi

# Passwordless sudo: the point of this example is that everyday work is not
# done as root, which only holds if becoming root is not a chore.
echo "$user ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/90-$user"
chmod 0440 "/etc/sudoers.d/90-$user"

# Start in the workspace. shell.cwd already puts 'idev shell' there; this is
# for a login shell reached another way.
printf 'cd %s\n' "${IDEV_WORKSPACE}" > "/home/$user/.bash_profile"
chown "$uid:$gid" "/home/$user/.bash_profile"

# The workspace is the host's directory. Nothing here chowns it: that would
# change the tree on the host.

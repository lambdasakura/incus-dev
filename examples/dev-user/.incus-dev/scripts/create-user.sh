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

# Refuse rather than shadow. Two accounts on one uid give that id two names,
# and id(1) answers with whichever getent returns first -- so the shell you
# land in disagrees with $USER about who you are. The base image is chosen so
# this does not arise; another one may need the id moved out of the way first.
existing="$(getent passwd "$uid" | cut -d: -f1 || true)"
if [ -n "$existing" ] && [ "$existing" != "$user" ]; then
    echo "uid $uid already belongs to '$existing' in this image." >&2
    echo "Use it (DEV_USER=$existing), or move it first:" >&2
    echo "  usermod -u <other uid> $existing" >&2
    exit 1
fi

if ! id "$user" >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash --uid "$uid" --gid "$group" "$user"
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

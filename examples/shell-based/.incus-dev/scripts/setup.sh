#!/bin/sh
# Setup specific to this project. Write it so it can be re-run.
set -eu

echo "setting up ${DEVKIT_PROJECT_NAME} in ${DEVKIT_INSTANCE} (mode=${SETUP_MODE:-default})"

# For example: do nothing when it is already there.
if [ ! -f /etc/profile.d/workspace.sh ]; then
    cat > /etc/profile.d/workspace.sh <<PROFILE
cd ${DEVKIT_WORKSPACE}
PROFILE
fi

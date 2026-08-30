#!/bin/sh
# プロジェクト固有のセットアップ。再実行できるように書くこと。
set -eu

echo "setting up ${DEVKIT_PROJECT_NAME} in ${DEVKIT_INSTANCE} (mode=${SETUP_MODE:-default})"

# 例: 既に存在すれば何もしない
if [ ! -f /etc/profile.d/workspace.sh ]; then
    cat > /etc/profile.d/workspace.sh <<PROFILE
cd ${DEVKIT_WORKSPACE}
PROFILE
fi

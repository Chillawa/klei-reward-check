#!/bin/sh
set -eu
cd -P "$(dirname "$0")/../desktop/core"
version=$(plutil -extract CFBundleShortVersionString raw ../macos/Info.plist)
build=$(plutil -extract CFBundleVersion raw ../macos/Info.plist)
numeric_version=$(printf '%s\n' "$version" | awk -F. -v build="$build" '
    function valid(n) { return n ~ /^(0|[1-9][0-9]*)$/ && n + 0 <= 65535 }
    NF < 2 || NF > 3 || !valid(build) { exit 1 }
    { for (i = 1; i <= NF; i++) if (!valid($i)) exit 1 }
    { print $1 "." $2 "." (NF == 3 ? $3 : 0) }
') || { echo 'Invalid Windows version or build number' >&2; exit 1; }
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM
llvm-rc /I windows /D "PRODUCT_VERSION_NUM=$(printf '%s' "$numeric_version" | tr . ,)" \
    /D "BUILD_NUMBER=$build" /D "PRODUCT_VERSION_TEXT=\"$version\\0\"" \
    /D "FILE_VERSION_TEXT=\"$numeric_version.$build\\0\"" /FO "$stage/rewards.res" windows/rewards.rc
llvm-cvtres /machine:X64 /timestamp:0 "/out:$stage/points_windows_amd64.syso" "$stage/rewards.res"
llvm-cvtres /machine:ARM64 /timestamp:0 "/out:$stage/points_windows_arm64.syso" "$stage/rewards.res"
cp "$stage"/*.syso .

#!/bin/sh
set -eu
cd -P "$(dirname "$0")/.."
project=$PWD
version=$(awk -F '[<>]' '/<key>CFBundleShortVersionString<\/key>/ { getline; print $3; exit }' desktop/macos/Info.plist)
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+(\.[0-9]+)?$' || { echo 'Invalid desktop release version' >&2; exit 1; }
out=${KLEI_RELEASE_DIR:-"$project/build/releases"}
mkdir -p "$out"
out=$(cd -P "$out" && pwd)
stage=$(mktemp -d "$out/.build.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM

compile_core() {
    platform=$1
    cpu=$2
    target=$3
    flags="-s -w -X main.releaseVersion=$version"
    if [ "$platform" = windows ]; then flags="$flags -H=windowsgui"; fi
    if [ -n "${KLEI_PUBLIC_FEED_URL:-}" ]; then
        case "$KLEI_PUBLIC_FEED_URL" in
            *[!a-zA-Z0-9:/_~.%+-]*) echo 'Invalid feed URL' >&2; exit 1 ;;
            https://*) flags="$flags -X klei-reward-check/desktop/internal/announcements.publicRewardFeedURL=$KLEI_PUBLIC_FEED_URL" ;;
            *) echo 'Feed URL must begin with https://' >&2; exit 1 ;;
        esac
    fi
    (cd "$project/desktop/core" && CGO_ENABLED=0 GOOS="$platform" GOARCH="$cpu" go build -trimpath -buildvcs=false -ldflags "$flags" -o "$target" .)
}

write_notices() {
    (
        cd "$project/desktop/core"
        CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go list -deps -f '{{with .Module}}{{if ne .Path "klei-reward-check/desktop"}}{{.Path}} {{.Dir}}{{end}}{{end}}' . > "$stage/modules"
        sort -u "$stage/modules" > "$stage/modules.sorted"
        printf 'Klei Points Auto-Claim\n'
        cat "$project/LICENSE"
        printf '\nArtwork\n'
        cat "$project/desktop/core/ui/ICON-NOTICE.txt"
        printf '\nGo\n'
        cat "$(go env GOROOT)/LICENSE"
        while read -r module module_dir; do
            [ -n "$module" ] || continue
            printf '\n%s\n' "$module"
            cat "$module_dir/LICENSE"
            for notice in NOTICE NOTICE.txt; do
                if [ -f "$module_dir/$notice" ]; then cat "$module_dir/$notice"; fi
            done
        done < "$stage/modules.sorted"
    ) > "$3"
}

build_desktop() {
    platform=$1
    cpu=$2
    case "$cpu" in amd64) label=x64; mac_arch=x86_64 ;; arm64) label=arm64; mac_arch=arm64 ;; esac
    case "$platform" in darwin) system=macOS ;; windows) system=Windows ;; linux) system=Linux ;; esac
    name="Klei-Points-Auto-Claim-$system-$label"
    folder="$stage/$name"
    mkdir -p "$folder"
    if [ "$platform" = darwin ]; then
        macos="$project/desktop/macos"
        app="$folder/Klei 点数自动领.app"
        contents="$app/Contents"
        mkdir -p "$contents/MacOS" "$contents/Helpers" "$contents/Resources"
        cp "$macos/Info.plist" "$contents/"
        cp "$macos/Rewards.icns" "$project/desktop/core/ui/klei-points.png" "$project/desktop/core/ui/translations.json" "$contents/Resources/"
        compile_core "$platform" "$cpu" "$contents/Helpers/klei-core"
        cp -R "$macos"/*.lproj "$contents/Resources/"
        write_notices "$platform" "$cpu" "$contents/Resources/Licenses.txt"
        executable=$(plutil -extract CFBundleExecutable raw "$macos/Info.plist")
        xcrun --sdk macosx swiftc -O -target "$mac_arch-apple-macosx13.0" "$macos/Panel.swift" "$macos/Localization.swift" -o "$contents/MacOS/$executable"
        xcrun --sdk macosx swiftc -O -target "$mac_arch-apple-macosx13.0" -module-name KleiLogin "$macos/login/main.swift" "$macos/login/LoginRoute.swift" "$macos/Localization.swift" -o "$contents/Helpers/klei-login"
        for part in klei-core klei-login; do
            codesign --force --sign - --timestamp=none --identifier "local.klei-reward-check.$part" "$contents/Helpers/$part"
        done
        codesign --force --sign - --timestamp=none "$app"
        codesign --verify --strict --deep --all-architectures "$app"
    else
        binary=klei-points-auto-claim
        if [ "$platform" = windows ]; then binary=$binary.exe; fi
        compile_core "$platform" "$cpu" "$folder/$binary"
        mkdir -p "$folder/licenses"
        write_notices "$platform" "$cpu" "$folder/licenses/Licenses.txt"
    fi
    (cd "$folder" && zip -q -r -X "$stage/$name.zip" .)
    mv -f "$stage/$name.zip" "$out/$name.zip"
    echo "$out/$name.zip"
}

case "${1:-all}" in
    macos) platforms=darwin ;; windows) platforms=windows ;; linux) platforms=linux ;; all) platforms='darwin windows linux' ;;
    *) echo 'Usage: package-desktop.sh [macos|windows|linux|all] [arm64|x64]' >&2; exit 2 ;;
esac
case "${2:-both}" in both) architectures='arm64 amd64' ;; arm64) architectures=arm64 ;; x64|amd64) architectures=amd64 ;; *) exit 2 ;; esac
for system_target in $platforms; do
    for architecture in $architectures; do
        build_desktop "$system_target" "$architecture"
    done
done

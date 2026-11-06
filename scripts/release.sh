#!/bin/bash
# Build, notarize, and publish the next patch release.
# Requires macOS, Go, gh, asc (profile sm), and a Developer ID certificate.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TAP_DIR=${TAP_DIR:-"$ROOT/../homebrew-tap"}
RELEASE_REPO=rijieli/uni-cli
ASC_PROFILE=sm
export ASC_PROFILE
cd "$ROOT"

for cmd in go git gh asc jq codesign security ditto shasum zip; do
  command -v "$cmd" >/dev/null || { echo "Missing command: $cmd" >&2; exit 1; }
done
gh auth status >/dev/null
test -d "$TAP_DIR/.git" || { echo "Tap checkout not found: $TAP_DIR" >&2; exit 1; }
test -z "$(git status --porcelain)" || { echo 'Source checkout must be clean; commit your changes first.' >&2; exit 1; }
test -z "$(git -C "$TAP_DIR" status --porcelain)" || { echo 'Tap checkout must be clean.' >&2; exit 1; }
git fetch --tags origin
git -C "$TAP_DIR" fetch origin main
test "$(git -C "$TAP_DIR" branch --show-current)" = main || { echo 'Tap must be on main.' >&2; exit 1; }
test "$(git -C "$TAP_DIR" rev-parse HEAD)" = "$(git -C "$TAP_DIR" rev-parse origin/main)" || { echo 'Tap must be up to date with origin/main.' >&2; exit 1; }

LATEST=$(git tag -l 'v[0-9]*' | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$/ { print substr($0, 2) }' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
LATEST="v$LATEST"
[[ "$LATEST" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || { echo 'No vMAJOR.MINOR.PATCH tag found.' >&2; exit 1; }
VERSION="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((10#${BASH_REMATCH[3]} + 1))"
TAG="v$VERSION"
RELEASE_TAG="$TAG"
if gh release view "$RELEASE_TAG" --repo "$RELEASE_REPO" >/dev/null 2>&1; then
  echo "Release $RELEASE_TAG already exists." >&2; exit 1
fi

if [ -z "${SIGNING_IDENTITY:-}" ]; then
  IDENTITIES=$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application: [^"]*\)".*/\1/p')
  [ "$(printf '%s\n' "$IDENTITIES" | grep -c 'Developer ID Application:' || true)" -eq 1 ] || { echo 'Set SIGNING_IDENTITY to your Developer ID Application certificate.' >&2; exit 1; }
  SIGNING_IDENTITY=$IDENTITIES
fi
AUTH_STATUS=$(asc auth status --output json)
STORED_IN=$(printf '%s' "$AUTH_STATUS" | jq -er --arg p "$ASC_PROFILE" '.credentials[] | select(.name == $p) | .storedIn') || { echo "Missing asc profile: $ASC_PROFILE" >&2; exit 1; }
if [ "$STORED_IN" != keychain ]; then export ASC_BYPASS_KEYCHAIN=1; fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/dist"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/arm64 windows/amd64; do
  os=${target%/*}; arch=${target#*/}; ext=
  [ "$os" != windows ] || ext=.exe
  name="uni-cli_${TAG}_${os}_${arch}"
  mkdir -p "$WORK/$name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags="-s -w -X main.version=$TAG" -o "$WORK/$name/uni-cli$ext" .
  cp README.md "$WORK/$name/"
  if [ "$os" = darwin ]; then
    codesign --force --sign "$SIGNING_IDENTITY" --options runtime --timestamp "$WORK/$name/uni-cli"
    codesign --verify --strict --verbose=2 "$WORK/$name/uni-cli"
    ditto -c -k --keepParent "$WORK/$name" "$WORK/notary.zip"
    asc notarization submit --file "$WORK/notary.zip" --wait
    rm "$WORK/notary.zip"
  fi
  if [ "$os" = windows ]; then
    (cd "$WORK" && zip -qr "dist/$name.zip" "$name")
  else
    tar -C "$WORK" -czf "$WORK/dist/$name.tar.gz" "$name"
  fi
done
(cd "$WORK/dist" && shasum -a 256 *.tar.gz *.zip > checksums.txt)
ARM_SHA=$(shasum -a 256 "$WORK/dist/uni-cli_${TAG}_darwin_arm64.tar.gz" | awk '{print $1}')
INTEL_SHA=$(shasum -a 256 "$WORK/dist/uni-cli_${TAG}_darwin_amd64.tar.gz" | awk '{print $1}')
LINUX_ARM_SHA=$(shasum -a 256 "$WORK/dist/uni-cli_${TAG}_linux_arm64.tar.gz" | awk '{print $1}')
LINUX_INTEL_SHA=$(shasum -a 256 "$WORK/dist/uni-cli_${TAG}_linux_amd64.tar.gz" | awk '{print $1}')

mkdir -p "$TAP_DIR/Formula"
cat > "$WORK/uni-cli.rb" <<FORMULA
class UniCli < Formula
  desc "Command-line client for remote MCP servers"
  homepage "https://github.com/rijieli/linear-cli"
  version "$VERSION"

  on_macos do
    on_arm do
      url "https://github.com/rijieli/uni-cli/releases/download/v#{version}/uni-cli_v#{version}_darwin_arm64.tar.gz"
      sha256 "$ARM_SHA"
    end
    on_intel do
      url "https://github.com/rijieli/uni-cli/releases/download/v#{version}/uni-cli_v#{version}_darwin_amd64.tar.gz"
      sha256 "$INTEL_SHA"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/rijieli/uni-cli/releases/download/v#{version}/uni-cli_v#{version}_linux_arm64.tar.gz"
      sha256 "$LINUX_ARM_SHA"
    end
    on_intel do
      url "https://github.com/rijieli/uni-cli/releases/download/v#{version}/uni-cli_v#{version}_linux_amd64.tar.gz"
      sha256 "$LINUX_INTEL_SHA"
    end
  end

  def install
    bin.install "uni-cli"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/uni-cli --version")
  end
end
FORMULA

echo "Publishing $TAG from $(git rev-parse --short HEAD) to $RELEASE_REPO"
git tag "$TAG"
git push origin "$TAG"
if [ -f "$ROOT/release-notes/$TAG.md" ]; then
  cat "$ROOT/release-notes/$TAG.md" > "$WORK/notes.md"
  printf '\nSource commit: %s\n' "$(git rev-parse HEAD)" >> "$WORK/notes.md"
else
  printf 'Source commit: %s\n' "$(git rev-parse HEAD)" > "$WORK/notes.md"
fi
gh release create "$RELEASE_TAG" "$WORK"/dist/* --repo "$RELEASE_REPO" --verify-tag --title "uni-cli $VERSION" --notes-file "$WORK/notes.md"
cp "$WORK/uni-cli.rb" "$TAP_DIR/Formula/uni-cli.rb"
git -C "$TAP_DIR" add Formula/uni-cli.rb
git -C "$TAP_DIR" commit -m "uni-cli: $VERSION"
git -C "$TAP_DIR" push origin main
echo "Released $TAG. Install with: brew install stillmake/tap/uni-cli"

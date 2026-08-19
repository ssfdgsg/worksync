#!/bin/sh
# Install worksync into ~/bin and add it to PATH.
# Portable: resolves the repo by this script's own location, so the whole
# repository (or just scripts/ + bin/) can be copied anywhere and run by
# any user. The binary is compiled for one platform; when this machine's
# platform differs, the script rebuilds from source with the local Go
# toolchain, or fails with a clear message when no Go is available.
# Usage: sh scripts/install-worksync.sh
set -e

REPO="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$REPO/bin/worksync"

# Map the current machine to a Go target platform.
os_="$(uname -s)"
arch="$(uname -m)"
case "$os_" in
  Darwin) goos="darwin" ;;
  Linux) goos="linux" ;;
  MINGW*|MSYS*|CYGWIN*) goos="windows" ;;
  *) goos="" ;;
esac
case "$arch" in
  arm64|aarch64) goarch="arm64" ;;
  x86_64|amd64) goarch="amd64" ;;
  *) goarch="" ;;
esac

rebuild_needed=0
if [ ! -f "$SRC" ]; then
  echo "no prebuilt binary at $SRC" >&2
  rebuild_needed=1
elif [ -n "$goos" ] && file "$SRC" | grep -q "Mach-O"; then
  [ "$goos" != "darwin" ] && rebuild_needed=1
elif [ -n "$goos" ] && file "$SRC" | grep -q "ELF"; then
  [ "$goos" != "linux" ] && rebuild_needed=1
fi
# PE (windows) binaries are not matched by the tests above; rebuild whenever
# the go target exists but is not one of the known prebuilt formats.
if [ "$rebuild_needed" = 0 ] && [ -n "$goos" ] \
    && ! file "$SRC" | grep -qE "Mach-O|ELF"; then
  rebuild_needed=1
fi

if [ "$rebuild_needed" = 1 ]; then
  if command -v go >/dev/null 2>&1; then
    echo "rebuilding worksync for $goos/$goarch..."
    (cd "$REPO" && GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -o bin/worksync ./cmd/worksync)
  else
    echo "error: bin/worksync is not built for this platform ($os_/$arch)" >&2
    echo "install a Go toolchain, then run: sh $0" >&2
    exit 1
  fi
fi

mkdir -p "$HOME/bin"
cp "$SRC" "$HOME/bin/worksync"
chmod 755 "$HOME/bin/worksync"

# Add to PATH in the shell rc matching the login shell (zsh first, then
# bash/posix); detect the interactive shell instead of hardcoding zsh.
shell_name="${SHELL##*/}"
rc=""
case "$shell_name" in
  zsh) rc="$HOME/.zshrc" ;;
  bash) rc="$HOME/.bashrc" ;;
  *) rc="$HOME/.profile" ;;
esac
# Create the rc file if it does not exist so a fresh machine gets PATH set
# without manual steps.
if [ ! -f "$rc" ]; then
  : > "$rc"
fi
if ! grep -q 'HOME/bin' "$rc" 2>/dev/null; then
  echo "export PATH=\"\$HOME/bin:\$PATH\"" >> "$rc"
fi
echo "installed: $HOME/bin/worksync (PATH line added to $rc)"
echo "run 'source $rc' then 'worksync help'"
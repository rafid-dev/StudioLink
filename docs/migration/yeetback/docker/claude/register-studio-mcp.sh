#!/bin/sh
# Registers the host's StudioLink bridge at user scope for SSH sessions,
# which don't get the container's environment. Runs as node at container
# start. User scope lives in ~/.claude.json, which is a symlink into the
# claude-home volume, so every session in that volume sees the same entry.
#
# The token comes from STUDIOLINK_TOKEN (compose env_file). It is written
# into the entry, so the entry is replaced on every start to follow a new
# token.
set -eu

NAME=Roblox_Studio
URL=http://host.docker.internal:44770/mcp

if [ -z "${STUDIOLINK_TOKEN:-}" ]; then
    echo "register-studio-mcp: STUDIOLINK_TOKEN is not set; run studiolink-host start on Windows, then recreate the container" >&2
    exit 1
fi
claude mcp remove -s user "$NAME" >/dev/null 2>&1 || true
claude mcp add-json -s user "$NAME" \
    "{\"type\":\"http\",\"url\":\"$URL\",\"headers\":{\"Authorization\":\"Bearer $STUDIOLINK_TOKEN\"}}" >/dev/null
echo "register-studio-mcp: $NAME (user scope) -> $URL"

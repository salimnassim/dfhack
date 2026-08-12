#!/usr/bin/env bash
set -euo pipefail

REPO="DFHack/dfhack"

TAG="${DFHACK_VERSION:-}"
if [[ -z "${TAG}" ]]; then
  TAG="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | jq -r '.tag_name')"
fi
if [[ -z "${TAG}" || "${TAG}" == "null" ]]; then
  echo "error: could not resolve a release tag" >&2
  exit 1
fi
echo "release tag: ${TAG}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

ARCHIVE_URL="https://github.com/${REPO}/archive/refs/tags/${TAG}.tar.gz"
echo "downloading: ${ARCHIVE_URL}"
curl -fsSL "${ARCHIVE_URL}" -o "${TMP_DIR}/dfhack-src.tar.gz"

echo "extracting tarball"
tar -xzf "${TMP_DIR}/dfhack-src.tar.gz" -C "${TMP_DIR}" --strip-components=1

echo "staging for buf"
mkdir -p proto/library proto/plugins
rm -f proto/library/*.proto proto/plugins/*.proto
cp "${TMP_DIR}"/library/proto/*.proto proto/library/
cp "${TMP_DIR}"/plugins/proto/*.proto proto/plugins/

echo "generating with buf"
buf generate

echo "done"

#!/usr/bin/env bash
# downloads DFHack protobuf definitions from the latest release and
# generates Go bindings using bufbuild/buf
#
# DFHACK_VERSION=53.15-r1 ./scripts/generate-proto.sh

set -euo pipefail

REPO="DFHack/dfhack"
BUF_IMAGE="bufbuild/buf:latest"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

for cmd in curl jq tar docker; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "error: required command '${cmd}' not found on PATH" >&2
    exit 1
  fi
done

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
rm -rf proto/library proto/plugins
mkdir -p proto/library proto/plugins
cp "${TMP_DIR}"/library/proto/*.proto proto/library/
cp "${TMP_DIR}"/plugins/proto/*.proto proto/plugins/

echo "generating with ${BUF_IMAGE}"
docker run --rm \
  --env HOME=/tmp \
  --volume "${ROOT_DIR}:/workspace" \
  --workdir /workspace \
  "${BUF_IMAGE}" generate

if command -v go >/dev/null 2>&1; then
  go mod tidy
fi

echo "done"

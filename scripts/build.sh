#!/usr/bin/env bash
set -euo pipefail

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_DATE=${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
OUTPUT=${OUTPUT:-proxyforge}

version_package=proxyforge/internal/version
ldflags="-s -w"
ldflags+=" -X ${version_package}.Version=${VERSION}"
ldflags+=" -X ${version_package}.Commit=${COMMIT}"
ldflags+=" -X ${version_package}.BuildDate=${BUILD_DATE}"

go build -trimpath -ldflags="${ldflags}" -o "${OUTPUT}" ./cmd/proxyforge

printf 'built %s: version=%s commit=%s build_date=%s\n' \
  "${OUTPUT}" "${VERSION}" "${COMMIT}" "${BUILD_DATE}"

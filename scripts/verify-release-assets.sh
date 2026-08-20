#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
dist_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/dist"

if [[ -n "${version}" ]]; then
  for os in darwin linux windows; do
    for arch in amd64 arm64; do
      asset="${dist_dir}/pulumi-resource-railway-v${version}-${os}-${arch}.tar.gz"
      if [[ ! -f "${asset}" ]]; then
        echo "missing release asset: ${asset}" >&2
        exit 1
      fi
    done
  done
else
  assets=("${dist_dir}"/pulumi-resource-railway-v*-*.tar.gz)
  if [[ "${#assets[@]}" -ne 6 ]]; then
    echo "expected 6 release archives, found ${#assets[@]}" >&2
    printf '%s\n' "${assets[@]}" >&2
    exit 1
  fi
fi

test -f "${dist_dir}/checksums.txt"

case "$(uname -s)" in
  Darwin) host_os="darwin" ;;
  Linux) host_os="linux" ;;
  *)
    echo "unsupported verification host: $(uname -s)" >&2
    exit 1
    ;;
esac
case "$(uname -m)" in
  x86_64) host_arch="amd64" ;;
  arm64 | aarch64) host_arch="arm64" ;;
  *)
    echo "unsupported verification architecture: $(uname -m)" >&2
    exit 1
    ;;
esac
if [[ -n "${version}" ]]; then
  host_asset="${dist_dir}/pulumi-resource-railway-v${version}-${host_os}-${host_arch}.tar.gz"
else
  host_assets=("${dist_dir}"/pulumi-resource-railway-v*-"${host_os}"-"${host_arch}".tar.gz)
  host_asset="${host_assets[0]}"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
tar -xzf "${host_asset}" -C "${tmp_dir}"
schema_version="$(pulumi package get-schema "${tmp_dir}/pulumi-resource-railway" | jq -r '.version')"
if [[ -n "${version}" && "${schema_version}" != "${version}" ]]; then
  echo "released provider reports ${schema_version}, expected ${version}" >&2
  exit 1
fi
if [[ -z "${schema_version}" || "${schema_version}" == "null" ]]; then
  echo "released provider reported an empty version" >&2
  exit 1
fi

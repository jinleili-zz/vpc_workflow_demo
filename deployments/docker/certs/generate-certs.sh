#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${SCRIPT_DIR}/generated"
CA_DIR="${OUT_DIR}/ca"
TOP_DIR="${OUT_DIR}/top"
AZ_DIR="${OUT_DIR}/az"

SERVER_NAMES=(
  "az-nsp-vpc-cn-beijing-1a"
  "az-nsp-vpc-cn-beijing-1b"
  "az-nsp-vpc-cn-shanghai-1a"
)

rm -rf "${OUT_DIR}"
mkdir -p "${CA_DIR}" "${TOP_DIR}" "${AZ_DIR}"

openssl req \
  -x509 \
  -newkey rsa:2048 \
  -nodes \
  -keyout "${CA_DIR}/ca.key" \
  -out "${CA_DIR}/ca.crt" \
  -days 3650 \
  -subj "/CN=nsp-demo-ca"

create_client_cert() {
  local name="$1"
  local dir="$2"

  mkdir -p "${dir}"

  openssl req \
    -newkey rsa:2048 \
    -nodes \
    -keyout "${dir}/${name}.key" \
    -out "${dir}/${name}.csr" \
    -subj "/CN=${name}"

  cat >"${dir}/${name}.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
EOF

  openssl x509 \
    -req \
    -in "${dir}/${name}.csr" \
    -CA "${CA_DIR}/ca.crt" \
    -CAkey "${CA_DIR}/ca.key" \
    -CAcreateserial \
    -out "${dir}/${name}.crt" \
    -days 825 \
    -extfile "${dir}/${name}.ext"

  rm -f "${dir}/${name}.csr" "${dir}/${name}.ext"
}

create_server_cert() {
  local dns_name="$1"
  local dir="$2"

  mkdir -p "${dir}"

  openssl req \
    -newkey rsa:2048 \
    -nodes \
    -keyout "${dir}/server.key" \
    -out "${dir}/server.csr" \
    -subj "/CN=${dns_name}"

  cat >"${dir}/server.ext" <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${dns_name}
EOF

  openssl x509 \
    -req \
    -in "${dir}/server.csr" \
    -CA "${CA_DIR}/ca.crt" \
    -CAkey "${CA_DIR}/ca.key" \
    -CAcreateserial \
    -out "${dir}/server.crt" \
    -days 825 \
    -extfile "${dir}/server.ext"

  rm -f "${dir}/server.csr" "${dir}/server.ext"
}

create_client_cert "top-client" "${TOP_DIR}"

for server_name in "${SERVER_NAMES[@]}"; do
  create_server_cert "${server_name}" "${AZ_DIR}/${server_name}"
done

echo "certificates generated under ${OUT_DIR}"

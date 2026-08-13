#!/usr/bin/env bash
set -euo pipefail

command -v curl >/dev/null
command -v jq >/dev/null
command -v openssl >/dev/null
command -v python3 >/dev/null

openssl req \
	-x509 \
	-newkey rsa:2048 \
	-nodes \
	-days 1 \
	-subj "/CN=tls-service.test" \
	-keyout tls.key \
	-out tls.crt \
	>/dev/null 2>&1

ARG BASE_IMAGE=debian:bookworm-slim
FROM ${BASE_IMAGE}

RUN if command -v apt-get >/dev/null; then \
		apt-get update \
		&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
			ca-certificates curl gpg iproute2 jq procps python3 \
		&& rm -rf /var/lib/apt/lists/*; \
	elif command -v dnf >/dev/null; then \
		dnf install -y ca-certificates gnupg2 iproute jq procps-ng python3 \
		&& dnf clean all; \
	elif command -v pacman >/dev/null; then \
		pacman -Sy --noconfirm ca-certificates curl gnupg iproute2 jq procps-ng python; \
	else \
		echo "unsupported package-test base image" >&2; \
		exit 1; \
	fi

COPY dist/ /packages/
COPY e2e/lib/common.sh /e2e/lib/common.sh
COPY e2e/cases/local_proxy_out_http/alm.hujson /e2e/cases/local_proxy_out_http/alm.hujson
COPY e2e/cases/default_tun_routing/ /e2e/cases/default_tun_routing/
COPY packaging/tests/smoke.sh /packaging-smoke.sh
COPY packaging/tests/integration.sh /packaging-integration.sh

ENTRYPOINT ["/packaging-smoke.sh"]

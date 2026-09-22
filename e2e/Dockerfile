# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36 AS build

WORKDIR /src

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	go install github.com/asciimoth/killswitch/cmd/killswitch@v0.1.5 \
	&& go install github.com/asciimoth/killswitch/cmd/killswitch-cli@v0.1.5 \
	&& go install -tags=e2e github.com/asciimoth/wgo/tests/amnesia/cmd/amnesia_e2e_tool@v0.7.0 \
	&& mv /go/bin/amnesia_e2e_tool /usr/local/bin/amnesia-e2e-tool

ARG GOST_COMMIT=91e12c44284163559e40cc142329409222cdd95e
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	curl -fsSL --retry 5 --retry-all-errors \
		"https://codeload.github.com/ginuerzh/gost/tar.gz/$GOST_COMMIT" \
		-o /tmp/gost.tar.gz \
	&& tar -xzf /tmp/gost.tar.gz -C /tmp \
	&& go build -C "/tmp/gost-$GOST_COMMIT" -o /go/bin/gost ./cmd/gost

ARG AMNEZIAWG_GO_COMMIT=b5928efb6ca19f0153958460c3d141f04abc5c2e
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	curl -fsSL --retry 5 --retry-all-errors \
		"https://codeload.github.com/amnezia-vpn/amneziawg-go/tar.gz/$AMNEZIAWG_GO_COMMIT" \
		-o /tmp/amneziawg-go.tar.gz \
	&& tar -xzf /tmp/amneziawg-go.tar.gz -C /tmp \
	&& go build -C "/tmp/amneziawg-go-$AMNEZIAWG_GO_COMMIT" -o /usr/local/bin/amneziawg-go . \
	&& printf '%s\n' "$AMNEZIAWG_GO_COMMIT" >/usr/local/share/amneziawg-go.commit

COPY go.work go.work.sum ./
COPY bin/go.mod bin/go.sum bin/
COPY certcat/go.mod certcat/
COPY lib/go.mod lib/go.sum lib/
RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

COPY bin bin
COPY certcat certcat
COPY lib lib

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	go build -C bin -o /usr/local/bin/almagest-cli ./almagest-cli

ARG E2E_COVERAGE=0
ARG E2E_COVERPKG=github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	if [ "$E2E_COVERAGE" = "1" ]; then \
		go build -C bin -tags=almagest_e2e -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /usr/local/bin/almagestd ./almagestd; \
	else \
		go build -C bin -tags=almagest_e2e -o /usr/local/bin/almagestd ./almagestd; \
	fi

FROM headscale/headscale:v0.29.3@sha256:0e7f1c6e4ce6c2a2a001103ecd3fa645a045adf30ac8a5234fe037b43000cd72 AS headscale

FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171

ARG E2E_SYSTEM_FLAVOR=direct
ARG E2E_SOCKS_COMPAT=0
ARG E2E_PACKET_ANALYZER=0
RUN rm -f /etc/apt/apt.conf.d/docker-clean \
	&& printf 'Binary::apt::APT::Keep-Downloaded-Packages "true";\n' >/etc/apt/apt.conf.d/keep-cache
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
	--mount=type=cache,target=/var/lib/apt,sharing=locked \
	apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		bash \
		ca-certificates \
		curl \
		dnsmasq \
		dnsutils \
		iproute2 \
		iputils-ping \
		jq \
		microsocks \
		netcat-openbsd \
		nftables \
		openssl \
		procps \
		python3 \
		socat \
		util-linux \
		wireguard-tools \
		wget \
	&& if [ "$E2E_SOCKS_COMPAT" = "1" ]; then \
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tor; \
	fi \
	&& if [ "$E2E_PACKET_ANALYZER" = "1" ]; then \
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tcpdump tshark; \
	fi \
	&& case "$E2E_SYSTEM_FLAVOR" in \
		direct) ;; \
		debian-resolvconf) \
			DEBIAN_FRONTEND=noninteractive apt-get install -y --download-only --no-install-recommends resolvconf; \
			dpkg --unpack /var/cache/apt/archives/resolvconf_*.deb; \
			sed -i 's@^[[:space:]]*ln -nsf .* /etc/resolv.conf@        true # Docker e2e keeps /etc/resolv.conf as a writable file@' /var/lib/dpkg/info/resolvconf.postinst; \
			dpkg --configure resolvconf; \
			;; \
		openresolv) \
			DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openresolv; \
			;; \
		systemd-resolved) \
			DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends dbus systemd systemd-resolved; \
			;; \
		*) printf 'unknown E2E_SYSTEM_FLAVOR=%s\n' "$E2E_SYSTEM_FLAVOR" >&2; exit 2 ;; \
	esac \
	&& chmod 0755 /

COPY --from=build /usr/local/bin/almagestd /usr/local/bin/almagestd
COPY --from=build /usr/local/bin/almagest-cli /usr/local/bin/almagest-cli
COPY --from=build /go/bin/killswitch /usr/local/bin/killswitch
COPY --from=build /go/bin/killswitch-cli /usr/local/bin/killswitch-cli
COPY --from=build /go/bin/gost /usr/local/bin/gost
COPY --from=build /usr/local/bin/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=build /usr/local/bin/amnesia-e2e-tool /usr/local/bin/amnesia-e2e-tool
COPY --from=build /usr/local/share/amneziawg-go.commit /usr/local/share/amneziawg-go.commit
COPY --from=headscale /ko-app/headscale /usr/local/bin/headscale
COPY e2e /e2e

WORKDIR /e2e
ENTRYPOINT ["/e2e/run-case.sh"]

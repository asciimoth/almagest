# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32

FROM golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c AS build

WORKDIR /src

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
	--mount=type=cache,target=/var/lib/apt,sharing=locked \
	apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		gcc \
		libseccomp-dev \
		pkg-config

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

COPY go.work ./
COPY bin/go.mod bin/go.sum bin/
COPY certcat/go.mod certcat/
COPY lib/go.mod lib/go.sum lib/
RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

COPY bin bin
COPY certcat certcat
COPY lib lib
COPY e2e/helpers e2e/helpers

ARG E2E_COVERAGE=0
ARG E2E_COVERPKG=github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	set -eu; \
	mkdir -p /out; \
	go build -C bin -o /out/almagest-cli ./almagest-cli; \
	CGO_ENABLED=0 go build -C bin -o /out/tor-e2e-helper ./cmd/tor-e2e-helper; \
	GOWORK=off GO111MODULE=off CGO_ENABLED=0 go build -C e2e/helpers/command-recorder \
		-o /out/almagest-e2e-command-recorder .; \
	if [ "$E2E_COVERAGE" = "1" ]; then \
		CGO_ENABLED=0 go build -C bin -tags=almagest_e2e -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /out/almagestd ./almagestd; \
		CGO_ENABLED=0 go build -C bin -tags=almagest_e2e -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /out/almagest-network-role ./cmd/almagest-network-role; \
		CGO_ENABLED=0 go build -C bin -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /out/almagest-config-writer-role ./cmd/almagest-config-writer-role; \
		CGO_ENABLED=0 go build -C bin -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /out/almagest-command-role ./cmd/almagest-command-role; \
	else \
		CGO_ENABLED=0 go build -C bin -tags=almagest_e2e -o /out/almagestd ./almagestd; \
		CGO_ENABLED=0 go build -C bin -tags=almagest_e2e -o /out/almagest-network-role ./cmd/almagest-network-role; \
		CGO_ENABLED=0 go build -C bin -o /out/almagest-config-writer-role ./cmd/almagest-config-writer-role; \
		CGO_ENABLED=0 go build -C bin -o /out/almagest-command-role ./cmd/almagest-command-role; \
	fi; \
	gcc -static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror -Wl,-z,relro,-z,now,-z,noexecstack \
		-o /out/almagest-linux-role-launcher \
		bin/native/linux-role/launcher.c $(pkg-config --cflags --libs libseccomp); \
	gcc -static-pie -O2 -D_FORTIFY_SOURCE=3 -fstack-protector-strong -fstack-clash-protection -fno-strict-overflow -Wformat=2 -Wall -Wextra -Werror -Wl,-z,relro,-z,now,-z,noexecstack \
		-o /out/almagest-subprogram-launcher \
		bin/native/subprogram/launcher.c $(pkg-config --cflags --libs libseccomp); \
	gcc -static -O2 -D_FORTIFY_SOURCE=3 -Wall -Wextra -Werror \
		-o /out/almagest-sandbox-e2e-helper \
		e2e/helpers/isolation-fixed-command.c

ARG E2E_LINUX_PROFILE=0
# Build focused native-profile assets only for the case-specific image. The
# linux_isolation_profile case runs them inside its own bounded cgroup.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	mkdir -p /qualification \
	&& if [ "$E2E_LINUX_PROFILE" = "1" ]; then \
		CGO_ENABLED=0 go build -C bin -trimpath -o /qualification/probe ./cmd/linux-profile-probe; \
		CGO_ENABLED=0 go build -C bin -trimpath -ldflags '-X main.probeMode=tsync-failure' -o /qualification/tsync-probe ./cmd/linux-profile-probe; \
		CGO_ENABLED=0 go build -C bin -trimpath -ldflags '-X main.probeMode=x32' -o /qualification/x32-probe ./cmd/linux-profile-probe; \
		CGO_ENABLED=0 go test -C bin -c -o /qualification/profile.test ./internal/isolation/linuxprofile; \
		gcc -static -O2 -Wall -Wextra -Werror -o /qualification/command e2e/helpers/isolation-fixed-command.c; \
		cp /out/almagest-linux-role-launcher /qualification/launcher; \
	fi

# Portable sandbox contracts and lifecycle tests run in the existing E2E image.
ARG E2E_SUBPROGRAM=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ "$E2E_SUBPROGRAM" = "1" ]; then \
        CGO_ENABLED=0 go test -C lib -c -o /qualification/sandbox.test ./nodekit; \
        CGO_ENABLED=0 go test -C bin -c -o /qualification/subprogram-policy.test ./internal/subprogram/policy; \
        CGO_ENABLED=0 go test -C bin -c -o /qualification/subprogram-manager.test ./internal/subprogram/manager; \
        CGO_ENABLED=0 go test -C bin -c -o /qualification/subprogram-direct.test ./internal/subprogram/direct; \
        CGO_ENABLED=0 go test -C bin -c -o /qualification/subprogram-service.test ./internal/subprogram/service; \
    fi

FROM headscale/headscale:v0.29.3@sha256:0e7f1c6e4ce6c2a2a001103ecd3fa645a045adf30ac8a5234fe037b43000cd72 AS headscale

FROM debian:bookworm-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171

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
		wget

# Keep the common runtime packages independent of case-specific build arguments.
# Variant images can then reuse the expensive package installation above.
ARG E2E_SYSTEM_FLAVOR=direct
ARG E2E_SOCKS_COMPAT=0
ARG E2E_PACKET_ANALYZER=0
ARG E2E_TOR_PRIVATE=0
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
	--mount=type=cache,target=/var/lib/apt,sharing=locked \
	if [ "$E2E_SYSTEM_FLAVOR" != "direct" ] \
		|| [ "$E2E_SOCKS_COMPAT" = "1" ] \
		|| [ "$E2E_PACKET_ANALYZER" = "1" ] \
		|| [ "$E2E_TOR_PRIVATE" = "1" ]; then \
		apt-get update; \
	fi \
	&& if [ "$E2E_SOCKS_COMPAT" = "1" ]; then \
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tor; \
	fi \
	&& if [ "$E2E_PACKET_ANALYZER" = "1" ]; then \
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tcpdump tshark; \
	fi \
	&& if [ "$E2E_TOR_PRIVATE" = "1" ]; then \
		DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
			git python3-cryptography python3-networkx python3-paramiko python3-rpyc python3-tomli-w python3-typeguard python3-typing-extensions python3-venv tor; \
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

ARG CHUTNEY_COMMIT=6cc158868d722e652975cb4efd5b278d95ff2fbb
RUN if [ "$E2E_TOR_PRIVATE" = "1" ]; then \
		git init /opt/chutney \
		&& git -C /opt/chutney remote add origin https://gitlab.torproject.org/tpo/core/chutney.git \
		&& git -C /opt/chutney fetch --depth 1 origin "$CHUTNEY_COMMIT" \
		&& git -C /opt/chutney checkout --detach FETCH_HEAD \
		&& rm -rf /opt/chutney/.git \
		&& python3 -m venv --system-site-packages /opt/chutney-venv \
		&& /opt/chutney-venv/bin/pip install --no-cache-dir \
			typeguard==4.4.4 typing_extensions==4.15.0 \
		&& useradd --create-home --uid 1000 tor-test; \
	fi

COPY --from=build \
	/go/bin/killswitch \
	/go/bin/killswitch-cli \
	/go/bin/gost \
	/usr/local/bin/amneziawg-go \
	/usr/local/bin/amnesia-e2e-tool \
	/usr/local/bin/
COPY --from=build /usr/local/share/amneziawg-go.commit /usr/local/share/amneziawg-go.commit
COPY --from=headscale /ko-app/headscale /usr/local/bin/headscale
COPY --from=build /out/ /usr/local/bin/
COPY --from=build /qualification /qualification
COPY --link e2e /e2e
COPY --link examples /usr/share/almagest/examples

WORKDIR /e2e
ENTRYPOINT ["/e2e/run-case.sh"]

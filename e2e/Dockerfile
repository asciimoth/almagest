FROM golang:1.26.6-bookworm AS build

WORKDIR /src
COPY . .

ARG E2E_COVERAGE=0
ARG E2E_COVERPKG=github.com/asciimoth/almagest/bin/...,github.com/asciimoth/almagest/lib/...,github.com/asciimoth/almagest/certcat/...

RUN if [ "$E2E_COVERAGE" = "1" ]; then \
		go build -C bin -cover -covermode=atomic -coverpkg="$E2E_COVERPKG" -o /usr/local/bin/almagestd ./almagestd; \
	else \
		go build -C bin -o /usr/local/bin/almagestd ./almagestd; \
	fi
RUN go build -C bin -o /usr/local/bin/almagest-cli ./almagest-cli
RUN go install github.com/asciimoth/killswitch/cmd/killswitch@v0.1.5
RUN go install github.com/asciimoth/killswitch/cmd/killswitch-cli@v0.1.5
RUN git clone --depth 1 https://github.com/amnezia-vpn/amneziawg-go.git /tmp/amneziawg-go \
	&& go build -C /tmp/amneziawg-go -o /usr/local/bin/amneziawg-go . \
	&& git -C /tmp/amneziawg-go rev-parse HEAD >/usr/local/share/amneziawg-go.commit

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		bash \
		ca-certificates \
		curl \
		dnsmasq \
		dnsutils \
		iproute2 \
		iputils-ping \
		jq \
		netcat-openbsd \
		nftables \
		openssl \
		procps \
		python3 \
		socat \
		util-linux \
		wireguard-tools \
		wget \
	&& rm -rf /var/lib/apt/lists/* \
	&& chmod 0755 /

COPY --from=build /usr/local/bin/almagestd /usr/local/bin/almagestd
COPY --from=build /usr/local/bin/almagest-cli /usr/local/bin/almagest-cli
COPY --from=build /go/bin/killswitch /usr/local/bin/killswitch
COPY --from=build /go/bin/killswitch-cli /usr/local/bin/killswitch-cli
COPY --from=build /usr/local/bin/amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=build /usr/local/share/amneziawg-go.commit /usr/local/share/amneziawg-go.commit
COPY e2e /e2e

WORKDIR /e2e
ENTRYPOINT ["/e2e/run-case.sh"]

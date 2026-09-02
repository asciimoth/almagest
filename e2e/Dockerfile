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
RUN go install -tags=e2e github.com/asciimoth/wgo/tests/amnesia/cmd/amnesia_e2e_tool@v0.7.0 \
	&& mv /go/bin/amnesia_e2e_tool /usr/local/bin/amnesia-e2e-tool
ARG AMNEZIAWG_GO_COMMIT=b5928efb6ca19f0153958460c3d141f04abc5c2e
RUN curl -fsSL --retry 5 --retry-all-errors \
		"https://codeload.github.com/amnezia-vpn/amneziawg-go/tar.gz/$AMNEZIAWG_GO_COMMIT" \
		-o /tmp/amneziawg-go.tar.gz \
	&& tar -xzf /tmp/amneziawg-go.tar.gz -C /tmp \
	&& go build -C "/tmp/amneziawg-go-$AMNEZIAWG_GO_COMMIT" -o /usr/local/bin/amneziawg-go . \
	&& printf '%s\n' "$AMNEZIAWG_GO_COMMIT" >/usr/local/share/amneziawg-go.commit

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
COPY --from=build /usr/local/bin/amnesia-e2e-tool /usr/local/bin/amnesia-e2e-tool
COPY --from=build /usr/local/share/amneziawg-go.commit /usr/local/share/amneziawg-go.commit
COPY e2e /e2e

WORKDIR /e2e
ENTRYPOINT ["/e2e/run-case.sh"]

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY . .

RUN go build -C bin -o /usr/local/bin/almagestd ./almagestd
RUN go build -C bin -o /usr/local/bin/almagest-cli ./almagest-cli
RUN go install github.com/asciimoth/killswitch/cmd/killswitch@v0.1.5
RUN go install github.com/asciimoth/killswitch/cmd/killswitch-cli@v0.1.5

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		bash \
		ca-certificates \
		curl \
		iproute2 \
		iputils-ping \
		jq \
		netcat-openbsd \
		nftables \
		procps \
		python3 \
		wget \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /usr/local/bin/almagestd /usr/local/bin/almagestd
COPY --from=build /usr/local/bin/almagest-cli /usr/local/bin/almagest-cli
COPY --from=build /go/bin/killswitch /usr/local/bin/killswitch
COPY --from=build /go/bin/killswitch-cli /usr/local/bin/killswitch-cli
COPY e2e /e2e

WORKDIR /e2e
ENTRYPOINT ["/e2e/run-case.sh"]

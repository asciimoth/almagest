# syntax=docker/dockerfile:1

FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36

RUN rm -f /etc/apt/apt.conf.d/docker-clean \
	&& apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		gcc \
		jq \
		libseccomp-dev \
		pkg-config \
		shellcheck \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.work ./
COPY bin/go.mod bin/go.sum bin/
COPY certcat/go.mod certcat/
COPY lib/go.mod lib/go.sum lib/
# Keep the module source in the image. The experiment runner executes race and
# vet checks with networking disabled before it starts any workload.
RUN go mod download

COPY bin bin
COPY certcat certcat
COPY contracts contracts
COPY examples examples
COPY lib lib
COPY dev/winvm dev/winvm
COPY dev/linux-isolation /experiment-src
COPY dev/data-path /data-path-src

RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/runtime-probe ./cmd/linux-isolation-probe \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/feature-probe ./cmd/linux-isolation-feature-probe \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/data-path-probe ./cmd/linux-isolation-data-path-probe \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/matcher-broker ./cmd/linux-isolation-matcher-broker \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/matcher-probe ./cmd/linux-isolation-matcher-probe \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/tun-broker ./cmd/linux-isolation-tun-broker \
	&& CGO_ENABLED=0 go build -C bin -trimpath -o /experiment/tun-probe ./cmd/linux-isolation-tun-probe \
	&& gcc -D_FORTIFY_SOURCE=3 -O2 -Wall -Wextra -Werror -o /experiment/launcher \
		/experiment-src/launcher.c $(pkg-config --cflags --libs libseccomp) \
	&& gcc -static -D_FORTIFY_SOURCE=3 -O2 -Wall -Wextra -Werror \
		-o /experiment/security-probe /data-path-src/security.c \
	&& shellcheck /experiment-src/run.sh /data-path-src/run.sh /data-path-src/security.sh

ENTRYPOINT []

# syntax=docker/dockerfile:1
FROM golang:1.26.6-bookworm@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
RUN apt-get update && apt-get install -y --no-install-recommends gcc libseccomp-dev pkg-config && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.work ./
COPY bin/go.mod bin/go.sum bin/
COPY certcat/go.mod certcat/
COPY lib/go.mod lib/go.sum lib/
RUN go mod download
COPY bin bin
COPY lib lib
COPY certcat certcat
COPY dev/linux-profiles dev/linux-profiles
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -C bin -trimpath -o /qualification/probe ./cmd/linux-profile-probe \
    && CGO_ENABLED=0 go build -C bin -trimpath -ldflags '-X main.probeMode=runtime-client' -o /qualification/runtime-client-probe ./cmd/linux-profile-probe \
    && CGO_ENABLED=0 go build -C bin -trimpath -ldflags '-X main.probeMode=tsync-failure' -o /qualification/tsync-probe ./cmd/linux-profile-probe \
    && CGO_ENABLED=0 go build -C bin -trimpath -ldflags '-X main.probeMode=x32' -o /qualification/x32-probe ./cmd/linux-profile-probe \
    && gcc -static -O2 -D_FORTIFY_SOURCE=3 -Wall -Wextra -Werror -o /qualification/launcher bin/native/linux-role/launcher.c $(pkg-config --cflags --libs libseccomp) \
    && gcc -static -O2 -Wall -Wextra -Werror -o /qualification/command dev/linux-profiles/command.c \
    && go test -C bin -c -o /qualification/profile.test ./internal/isolation/linuxprofile
ENTRYPOINT []

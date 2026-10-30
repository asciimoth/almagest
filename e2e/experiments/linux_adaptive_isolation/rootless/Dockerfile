ARG BUILDER_IMAGE=golang:1.26.10-bookworm
ARG RUNTIME_IMAGE=almagest-e2e:local
FROM ${BUILDER_IMAGE} AS build

COPY probe.c /src/probe.c
RUN gcc -static -O2 -D_FORTIFY_SOURCE=3 -Wall -Wextra -Werror \
	-o /rootless-probe /src/probe.c

FROM ${RUNTIME_IMAGE}

COPY --from=build /rootless-probe /usr/local/bin/rootless-probe
ENTRYPOINT ["/usr/local/bin/rootless-probe"]

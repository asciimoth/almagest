# syntax=docker/dockerfile:1

ARG BUILDER_IMAGE=golang@sha256:116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36
ARG RUNTIME_IMAGE=almagest-capability-runtime:bca0ffaf64c9

FROM ${BUILDER_IMAGE} AS build
COPY cap_probe.c /src/cap_probe.c
RUN mkdir -p /out \
	&& gcc -static -O2 -D_GNU_SOURCE -Wall -Wextra -Werror \
	-o /out/cap-probe /src/cap_probe.c

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/cap-probe /usr/local/bin/cap-probe
RUN cp /usr/local/bin/cap-probe /usr/local/bin/cap-probe-file \
	&& setcap cap_net_admin=ep /usr/local/bin/cap-probe-file \
	&& test "$(getcap /usr/local/bin/cap-probe-file)" = \
		'/usr/local/bin/cap-probe-file cap_net_admin=ep'
ENTRYPOINT ["/usr/local/bin/cap-probe"]

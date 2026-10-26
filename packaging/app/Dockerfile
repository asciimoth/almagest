FROM rust:1.90-bookworm@sha256:3914072ca0c3b8aad871db9169a651ccfce30cf58303e5d6f2db16d1d8a7e58f AS build

RUN apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends \
		libdbus-1-dev libgtk-3-dev libwebkit2gtk-4.1-dev pkg-config \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /source
COPY . .
RUN cargo build --manifest-path app/src-tauri/Cargo.toml --release --locked

FROM scratch AS export
COPY --from=build /source/app/src-tauri/target/release/almagest-app /almagest-app

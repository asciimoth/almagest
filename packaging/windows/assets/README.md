# Windows package assets

Phase 4 will put Windows-installer-specific artwork in this directory. Do not
put generated WiX output or runtime dependencies here. The build must take the
runtime dependencies from the pinned Nix outputs described in
`../contract.json`.

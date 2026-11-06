# Usage:
# - Build the package: nix build .#almagest
# - Run the daemon from the flake: nix run github:asciimoth/almagest#almagest
# - Run the client: nix shell github:asciimoth/almagest#almagest -c almagest-cli --help
# - Add to a temporary shell: nix shell github:asciimoth/almagest#almagest
# - Add to a profile: nix profile add github:asciimoth/almagest#almagest
# - Install the package in a NixOS configuration:
#     environment.systemPackages = [ inputs.almagest.packages.${pkgs.system}.almagest ];
# - Enable and configure the NixOS service from this flake:
#     imports = [ inputs.almagest.nixosModules.almagestd ];
#     services.almagestd = {
#       enable = true;
#       isolation = "off"; # Use "auto" or "strict" to enable isolation.
#       settings.Config.Save.IdleMilliseconds = 500;
#     };
#   Generated settings are read-only and are stored in the Nix store.
# - Initialize a mutable configuration from Nix settings:
#     services.almagestd = {
#       enable = true;
#       isolation = "off";
#       settings = null;
#       initialSettings = {
#         Config.Save.IdleMilliseconds = 500;
#         nodes.proxy.Class = "almagest/SOCKS5Server";
#       };
#       configFile = "/var/lib/almagest/alm.hujson";
#     };
#   The service creates configFile only when it does not exist. Almagest can
#   then update the file. The service copies Nix-managed protectedSettings to
#   a private runtime file before startup.
#   For protected secrets, use protectedConfigFile and set protectedSettings
#   to null.
{
  description = "almagest dev env";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils = {
      url = "github:numtide/flake-utils";
    };
    pre-commit-hooks = {
      url = "github:cachix/pre-commit-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    ghrepo = {
      url = "github:asciimoth/ghrepo";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
      inputs.pre-commit-hooks.follows = "pre-commit-hooks";
    };
  };
  outputs = {
    self,
    nixpkgs,
    flake-utils,
    pre-commit-hooks,
    ghrepo,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };
      staticLibseccomp = pkgs.libseccomp.overrideAttrs (_: {
        dontDisableStatic = true;
      });
      linuxIsolationSupported = system == "x86_64-linux";
      cleanProjectSource = pkgs.lib.cleanSourceWith {
        src = ./.;
        filter = path: type:
          !(type == "directory" && builtins.elem (baseNameOf (toString path)) [
            ".direnv"
            ".git"
            "build"
            "dist"
            "node_modules"
            "target"
          ]);
      };
      localeEntries = builtins.readDir ./locales;
      productionLocaleNames = builtins.filter (name:
        localeEntries.${name} == "directory"
        && !(builtins.fromJSON (builtins.readFile (./locales + "/${name}/manifest.json"))).development_only
      ) (builtins.attrNames localeEntries);
      productionLocales = pkgs.runCommand "almagest-production-locales" {} ''
        mkdir "$out"
        ${pkgs.lib.concatMapStringsSep "\n" (name: ''
          cp -r ${./locales + "/${name}"} "$out/${name}"
        '') productionLocaleNames}
      '';

      linuxIsolationLaunchers = pkgs.stdenv.mkDerivation {
        pname = "almagest-linux-isolation-launchers";
        version = self.shortRev or self.dirtyShortRev or "dev";
        src = ./.;
        nativeBuildInputs = [ pkgs.pkg-config ];
        buildInputs = [
          staticLibseccomp
          staticLibseccomp.lib
          pkgs.glibc.static
        ];
        dontConfigure = true;
        buildPhase = ''
          runHook preBuild
          common_flags=(
            -static-pie
            -O2
            -D_FORTIFY_SOURCE=3
            -fstack-protector-strong
            -fstack-clash-protection
            -fno-strict-overflow
            -Wformat=2
            -Wall
            -Wextra
            -Werror
            -Wl,-z,relro,-z,now,-z,noexecstack
            -L${pkgs.glibc.static}/lib
          )
          $CC "''${common_flags[@]}" -o almagest-linux-role-launcher \
            bin/native/linux-role/launcher.c \
            $(pkg-config --cflags --libs libseccomp)
          $CC "''${common_flags[@]}" -o almagest-subprogram-launcher \
            bin/native/subprogram/launcher.c \
            $(pkg-config --cflags --libs libseccomp)
          runHook postBuild
        '';
        installPhase = ''
          runHook preInstall
          install -Dm0755 almagest-linux-role-launcher \
            "$out/bin/almagest-linux-role-launcher"
          install -Dm0755 almagest-subprogram-launcher \
            "$out/bin/almagest-subprogram-launcher"
          runHook postInstall
        '';
      };

      panelStatic = pkgs.buildNpmPackage {
        pname = "almagest-panel-static";
        version = self.shortRev or self.dirtyShortRev or "dev";
        src = cleanProjectSource;
        npmRoot = "panel";
        npmDeps = pkgs.fetchNpmDeps {
          src = ./panel;
          hash = "sha256-zhrrb032RbHs2JGjrsr9Y1X7exTZDU5hK1QhKJ3NsDo=";
        };
        buildPhase = ''
          runHook preBuild
          (cd panel && npm run build)
          runHook postBuild
        '';
        installPhase = ''
          runHook preInstall
          mkdir -p "$out/daemon" "$out/app"
          cp -r bin/almagestd/static/. "$out/daemon/"
          cp -r app/frontend/. "$out/app/"
          runHook postInstall
        '';
      };

      almagest = pkgs.buildGoModule {
        pname = "almagest";
        version = self.shortRev or self.dirtyShortRev or "dev";
        src = ./.;
        modRoot = "bin";
        env.CGO_ENABLED = 0;
        vendorHash = "sha256-/Q3aeOtdI0Lxk0dDiV5S8s3JaWkOA/jPniQCwcINhjk=";
        subPackages = [
          "almagestd"
          "almagest-cli"
        ] ++ pkgs.lib.optionals linuxIsolationSupported [
          "cmd/almagest-network-role"
          "cmd/almagest-config-writer-role"
          "cmd/almagest-command-role"
          "cmd/almagest-isolation-storage"
        ];
        nativeBuildInputs = [ pkgs.makeWrapper ];
        nativeInstallCheckInputs = [ pkgs.diffutils ];
        preBuild = ''
          cp -r ${panelStatic}/daemon/. ./almagestd/static
        '';
        postInstall = ''
          mkdir -p "$out/share/almagest"
          cp -r ${./examples} "$out/share/almagest/examples"
          cp -r ${productionLocales} "$out/share/almagest/locales"
          mkdir -p "$out/libexec/almagest"
          mv "$out/bin/almagestd" "$out/libexec/almagest/almagestd"
          ${pkgs.lib.optionalString linuxIsolationSupported ''
            mv \
              "$out/bin/almagest-network-role" \
              "$out/bin/almagest-config-writer-role" \
              "$out/bin/almagest-command-role" \
              "$out/bin/almagest-isolation-storage" \
              "$out/libexec/almagest/"
            cp -r ${linuxIsolationLaunchers}/bin/. "$out/libexec/almagest/"
          ''}
          makeWrapper "$out/libexec/almagest/almagestd" "$out/bin/almagestd" \
            --set-default ALMAGEST_EXAMPLES_DIR "$out/share/almagest/examples" \
            --set-default ALMAGEST_LOCALES_DIR "$out/share/almagest/locales"
        '';
        doInstallCheck = true;
        installCheckPhase = ''
          runHook preInstallCheck
          diff --brief --recursive ${productionLocales} "$out/share/almagest/locales"
          diff --brief --recursive ${./examples} "$out/share/almagest/examples"
          test -f "$out/share/almagest/locales/en/manifest.json"
          test -f "$out/share/almagest/locales/en/docs/index.json"
          test ! -e "$out/share/almagest/locales/lorem"
          test -x "$out/bin/almagest-cli"
          test -x "$out/libexec/almagest/almagestd"
          ${pkgs.lib.optionalString linuxIsolationSupported ''
            for binary in \
              almagest-network-role \
              almagest-config-writer-role \
              almagest-command-role \
              almagest-isolation-storage \
              almagest-linux-role-launcher \
              almagest-subprogram-launcher; do
              test -x "$out/libexec/almagest/$binary"
            done
          ''}
          "$out/bin/almagestd" --help >/dev/null
          runHook postInstallCheck
        '';
        meta = with pkgs.lib; {
          description = "Userspace network graph daemon and administration client";
          homepage = "https://github.com/asciimoth/almagest";
          license = licenses.gpl3Plus;
          mainProgram = "almagestd";
          platforms = platforms.linux;
        };
      };

      almagestApp = pkgs.rustPlatform.buildRustPackage {
        pname = "almagest-app";
        version = self.shortRev or self.dirtyShortRev or "dev";
        src = cleanProjectSource;
        cargoRoot = "app/src-tauri";
        buildAndTestSubdir = "app/src-tauri";
        cargoHash = "sha256-NTLUknPahM93/H63CZeJqGqUeE+B4qUJPRPKn+9bSoo=";
        nativeBuildInputs = [
          pkgs.pkg-config
          pkgs.wrapGAppsHook3
        ];
        buildInputs = [
          pkgs.gtk3
          pkgs.libsoup_3
          pkgs.webkitgtk_4_1
        ];
        preBuild = ''
          rm -rf app/frontend
          mkdir -p app/frontend
          cp -r ${panelStatic}/app/. app/frontend/
        '';
        postInstall = ''
          install -Dm0644 ${./packaging/linux/almagest-app.desktop} \
            "$out/share/applications/almagest-app.desktop"
          install -Dm0644 ${./packaging/linux/almagest-app-autostart.desktop} \
            "$out/etc/xdg/autostart/almagest-app.desktop"
          install -Dm0644 app/src-tauri/icons/128x128.png \
            "$out/share/icons/hicolor/128x128/apps/almagest.png"
          install -Dm0644 ${./packaging/linux/app-README.md} \
            "$out/share/doc/almagest-app/README.md"
          install -Dm0644 ${./LICENSE} \
            "$out/share/licenses/almagest-app/LICENSE"
        '';
        doInstallCheck = true;
        installCheckPhase = ''
          runHook preInstallCheck
          test -x "$out/bin/almagest-app"
          test -f "$out/share/applications/almagest-app.desktop"
          test -f "$out/etc/xdg/autostart/almagest-app.desktop"
          "$out/bin/almagest-app" --help >/dev/null
          runHook postInstallCheck
        '';
        meta = with pkgs.lib; {
          description = "Desktop client for the Almagest administration API";
          homepage = "https://github.com/asciimoth/almagest";
          license = licenses.gpl3Plus;
          mainProgram = "almagest-app";
          platforms = ["x86_64-linux"];
        };
      };

      markdownFormatter = pkgs.python3.withPackages (
        pythonPackages: with pythonPackages; [
          mdformat
          mdformat-gfm
          mdformat-gfm-alerts
        ]
      );

      psScriptAnalyzer = pkgs.stdenvNoCC.mkDerivation {
        pname = "PSScriptAnalyzer";
        version = "1.25.0";
        src = pkgs.fetchurl {
          url = "https://github.com/PowerShell/PSScriptAnalyzer/releases/download/1.25.0/PSScriptAnalyzer.1.25.0.nupkg";
          hash = "sha256-FOY0yCjrmO+59AspGLqQ8TntXszfZjoqdHc22ZaZXWA=";
        };
        nativeBuildInputs = [pkgs.unzip];
        dontUnpack = true;
        installPhase = ''
          runHook preInstall
          mkdir -p "$out/share/powershell/Modules/PSScriptAnalyzer"
          unzip -q "$src" -d "$out/share/powershell/Modules/PSScriptAnalyzer"
          runHook postInstall
        '';
      };

      winvmLintHook = pkgs.writeShellScript "winvm-lint-hook" ''
        export PATH=${pkgs.lib.makeBinPath [pkgs.just pkgs.powershell pkgs.ruff pkgs.shellcheck]}:$PATH
        export PSSCRIPTANALYZER_PATH=${psScriptAnalyzer}/share/powershell/Modules/PSScriptAnalyzer/PSScriptAnalyzer.psd1
        just lint-winvm
      '';

      goHookFiles = "(^|/)(.*\\.go|go\\.(mod|sum))$|^flake\\..*$";

      gofmtHook = pkgs.writeShellScript "gofmt-hook" ''
        if printf '%s\n' "$@" | ${pkgs.gnugrep}/bin/grep -q '^flake\.'; then
          ${pkgs.git}/bin/git ls-files -z -- '*.go' \
            | ${pkgs.findutils}/bin/xargs -0 -r ${pkgs.go}/bin/gofmt -w
          exit
        fi

        for file in "$@"; do
          if [[ "$file" == *.go ]]; then
            ${pkgs.go}/bin/gofmt -w "$file"
          fi
        done
      '';

      goDirectoryHook = name: command: pkgs.writeShellScript name ''
        if printf '%s\n' "$@" | ${pkgs.gnugrep}/bin/grep -q '^flake\.'; then
          for dir in lib bin certcat; do
            (cd "$dir" && ${command})
          done
          exit
        fi

        printf '%s\n' "$@" \
          | ${pkgs.findutils}/bin/xargs -n1 ${pkgs.coreutils}/bin/dirname \
          | ${pkgs.coreutils}/bin/sort -u \
          | while read -r dir; do (cd "$dir" && ${command}); done
      '';

      golangciLintHook = goDirectoryHook "golangci-lint-hook" "${pkgs.golangci-lint}/bin/golangci-lint run ./...";
      govetHook = goDirectoryHook "govet-hook" "${pkgs.go}/bin/go vet ./...";

      checks = {
        pre-commit-check = pre-commit-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            actionlint.enable = true;
            commitizen.enable = true;
            typos.enable = true;
            typos-commit = {
              enable = true;
              description = "Find typos in commit message";
              entry = let script = pkgs.writeShellScript "typos-commit" ''
                typos "$1"
              ''; in builtins.toString script;
              stages = [ "commit-msg" ];
            };
            govet = {
              enable = true;
              entry = builtins.toString govetHook;
              files = goHookFiles;
            };
            gofmt = {
              enable = true;
              entry = builtins.toString gofmtHook;
              files = goHookFiles;
            };
            golangci-lint = {
              enable = true;
              entry = builtins.toString golangciLintHook;
              files = goHookFiles;
            };
            markdownlint = {
              enable = true;
              excludes = [ "^locales/lorem/docs/pages/guides/markdown-features\\.md$" ];
              settings.configuration = {
                default = true;
                MD013 = {
                  code_blocks = false;
                  line_length = 80;
                  tables = false;
                };
                MD033.allowed_elements = [
                  "animateTransform"
                  "circle"
                  "img"
                  "svg"
                ];
                MD041 = false;
              };
            };
            ripsecrets.enable = true;
            gotidy = {
              enable = true;
              description = "Makes sure go.mod matches the source code";
              files = goHookFiles;
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                just tidy
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            golangtest = {
              enable = true;
              description = "go test ./... --race";
              files = goHookFiles;
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "gotesthook" ''
                just test-go
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            govulncheck = {
              enable = true;
              description = "Run govulncheck";
              files = goHookFiles;
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "govulncheckhook" ''
                just vulncheck
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            winvm-lint = {
              enable = true;
              description = "Lint the Windows VM harness";
              entry = builtins.toString winvmLintHook;
              files = "^(flake\\..*$|justfile$|dev/winvm/|scripts/(lint-windows|test-e2e-platform|test-windows|test-windows-flow)\\.sh$)";
              pass_filenames = false;
              stages = [ "pre-commit" ];
            };
          };
        };
      };

      testChromium = pkgs.writeShellScript "almagest-test-chromium" ''
        exec ${pkgs.chromium}/bin/chromium --password-store=basic "$@"
      '';

      driverRevision = "5b6f46cde692acb77ee74b37b9fd3f1678c45a52";
      driverSourceRevision = "0a0eb97f67d1dbcb3d08bda66d3b24f465d95475";
      fetchDriverFile = architecture: file: hash: pkgs.fetchurl {
        url = "https://raw.githubusercontent.com/mullvad/mullvadvpn-app-binaries/${driverRevision}/${architecture}-pc-windows-msvc/split-tunnel/${file}";
        inherit hash;
      };
      windowsSplitTunnelDrivers = pkgs.runCommand "almagest-split-driver-1.3.0.0" {} ''
        install -Dm0444 ${fetchDriverFile "x86_64" "mullvad-split-tunnel.sys" "sha256-EM8lu8/lH9Zjof7IipjpuFjzpXlYm7LsSWtm5P3RsgE="} $out/amd64/mullvad-split-tunnel.sys
        install -Dm0444 ${fetchDriverFile "x86_64" "mullvad-split-tunnel.inf" "sha256-PdWQXl+5jWGpQqM+jJpboHw6LeHk8xnh/sPlTfZZFgg="} $out/amd64/mullvad-split-tunnel.inf
        install -Dm0444 ${fetchDriverFile "x86_64" "mullvad-split-tunnel.cat" "sha256-xZmSagMn164GtTT0zQOdswOS4Yl7udA+T+w2MXRKTm0="} $out/amd64/mullvad-split-tunnel.cat
        install -Dm0444 ${fetchDriverFile "aarch64" "mullvad-split-tunnel.sys" "sha256-avizv+WqCV1Sdhh1WMfH06PgwXSzRAbNbEs/jm/6ZTQ="} $out/arm64/mullvad-split-tunnel.sys
        install -Dm0444 ${fetchDriverFile "aarch64" "mullvad-split-tunnel.inf" "sha256-C/2wROQFNdq76zYgtlXBVjcUs6bzIx00kslyxujepvE="} $out/arm64/mullvad-split-tunnel.inf
        install -Dm0444 ${fetchDriverFile "aarch64" "mullvad-split-tunnel.cat" "sha256-w9J2NnOeuqfd42nREzR96D4+IXO4pRK7iGPRGxSN584="} $out/arm64/mullvad-split-tunnel.cat
        install -Dm0444 ${pkgs.fetchurl {
          url = "https://raw.githubusercontent.com/mullvad/win-split-tunnel/${driverSourceRevision}/LICENSE-MPL.txt";
          hash = "sha256-ZqMQfVrWoFiqt1PqrCBHzLLtDjlGXdD+WETaPjANUXI=";
        }} $out/licenses/mullvad-LICENSE-MPL.txt
      '';
      wintunArchive = pkgs.fetchurl {
        url = "https://www.wintun.net/builds/wintun-0.14.1.zip";
        hash = "sha256-B8JWGF1u42UuCfpVwLZz4mJLVl4CxLkJHHnKfS8k71E=";
      };
      windowsWintun = pkgs.runCommand "almagest-wintun-0.14.1" {nativeBuildInputs = [pkgs.unzip];} ''
        unzip -q ${wintunArchive} -d unpacked
        install -Dm0444 unpacked/wintun/bin/amd64/wintun.dll $out/amd64/wintun.dll
        install -Dm0444 unpacked/wintun/bin/arm64/wintun.dll $out/arm64/wintun.dll
        install -Dm0444 unpacked/wintun/LICENSE.txt $out/licenses/wintun-LICENSE.txt
      '';
    in {
      packages = {
        inherit almagest;
        almagestd = almagest;
        default = almagest;

        windows-split-tunnel-drivers = windowsSplitTunnelDrivers;
        windows-wintun = windowsWintun;
        windows-runtime-dependencies = pkgs.symlinkJoin {
          name = "almagest-windows-runtime-dependencies";
          paths = [windowsSplitTunnelDrivers windowsWintun];
        };

        # Keep the old test-oriented output names for existing commands.
        windows-test-drivers = windowsSplitTunnelDrivers;
        windows-test-wintun = windowsWintun;
      } // pkgs.lib.optionalAttrs (system == "x86_64-linux") {
        almagest-app = almagestApp;
      };
      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        CHROME_BIN = testChromium;
        MULLVAD_SPLIT_TUNNEL_DRIVER_DIR = windowsSplitTunnelDrivers;
        SYSNET_WINDOWS_WINTUN_DIR = windowsWintun;
        PSSCRIPTANALYZER_PATH = "${psScriptAnalyzer}/share/powershell/Modules/PSScriptAnalyzer/PSScriptAnalyzer.psd1";
        ALMAGEST_GLIBC_STATIC_LDFLAG = pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isLinux "-L${pkgs.glibc.static}/lib";
        WINVM_OVMF_CODE = pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.OVMF.fd}/FV/OVMF_CODE.fd";
        WINVM_OVMF_VARS = pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.OVMF.fd}/FV/OVMF_VARS.fd";
        buildInputs = with pkgs; [
          actionlint
          go
          golangci-lint
          govulncheck
          gopls

          goreleaser
          gh
          gnupg

          typos
          commitizen
          markdownFormatter
          markdownlint-cli
          shellcheck
          powershell
          ripsecrets
          ruff

          just
          python3
          jq
          git
          curl
          openssh
          util-linux

          nodejs_24
          pnpm
          typescript

          # for the Tauri desktop client
          cargo
          cargo-tauri
          clippy
          rustc
          rustfmt

          wireguard-tools
          xxd

          # for browser/e2e tests
          chromium

          tor
          obfs4
        ] ++ [ ghrepo.packages.${system}.ghrepo ] ++ pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [
          docker
          glib-networking
          gtk3
          libsoup_3
          pkg-config
          webkitgtk_4_1
        ] ++ pkgs.lib.optionals (system == "x86_64-linux") [
          staticLibseccomp
          staticLibseccomp.lib
          pkgs.qemu
          pkgs.xorriso
          pkgs.OVMF
        ];
      };
    })
    // {
      nixosModules.almagestd = {
        config,
        lib,
        pkgs,
        ...
      }: let
        cfg = config.services.almagestd;
        jsonFormat = pkgs.formats.json {};
        generatedConfig = jsonFormat.generate "almagestd.json" (
          (if cfg.settings == null then {} else cfg.settings)
          // { Protected = if cfg.protectedSettings == null then {} else cfg.protectedSettings; }
        );
        initialConfig = jsonFormat.generate "almagestd-initial.json" cfg.initialSettings;
        generatedProtectedConfig = jsonFormat.generate "almagestd-protected.json" cfg.protectedSettings;
        useGeneratedConfig = cfg.configFile == null;
        runtimeProtectedConfig = "/run/almagest/protected.json";
        protectedConfig =
          if cfg.protectedConfigFile == null
          then runtimeProtectedConfig
          else cfg.protectedConfigFile;
      in {
        options.services.almagestd = {
          enable = lib.mkEnableOption "Almagest network daemon";

          package = lib.mkOption {
            type = lib.types.package;
            default = self.packages.${pkgs.stdenv.hostPlatform.system}.almagest;
            defaultText = lib.literalExpression "inputs.almagest.packages.\${pkgs.stdenv.hostPlatform.system}.almagest";
            description = "Package that provides almagestd and almagest-cli.";
          };

          settings = lib.mkOption {
            type = lib.types.nullOr jsonFormat.type;
            default = {};
            example = lib.literalExpression ''
              {
                nodes.proxy = {
                  Class = "almagest/SOCKS5Server";
                };
              }
            '';
            description = ''
              Regular daemon configuration. The module renders this value as
              JSON. This generated configuration is read-only at run time.
              Set this option to null to use configFile.
            '';
          };

          initialSettings = lib.mkOption {
            type = lib.types.nullOr jsonFormat.type;
            default = null;
            example = lib.literalExpression ''
              {
                nodes.proxy = {
                  Class = "almagest/SOCKS5Server";
                };
              }
            '';
            description = ''
              Initial regular daemon configuration for configFile. Before the
              service starts, the module copies this value to configFile only
              when that file does not exist. Almagest can then update the file.
              Set settings to null when this option is used.
            '';
          };

          protectedSettings = lib.mkOption {
            type = lib.types.nullOr jsonFormat.type;
            default = {
              Admin = {
                Unix = "/run/almagest/admin.sock";
                UnixGroup = "almagest";
              };
              Sandbox = {
                Mode = "auto";
                DiscoverSystemPrograms = true;
              };
              System.Type = "native";
            };
            description = ''
              Protected daemon configuration. The module renders this value
              as JSON and copies it to a private runtime file when configFile
              is used. Values in this option are stored in the Nix store. Use
              protectedConfigFile for secrets.
            '';
          };

          configFile = lib.mkOption {
            type = lib.types.nullOr lib.types.path;
            default = null;
            example = "/etc/almagest/alm.hujson";
            description = ''
              Path to a writable regular HuJSON or JSON configuration file.
              Set settings to null when this option is used.
            '';
          };

          protectedConfigFile = lib.mkOption {
            type = lib.types.nullOr lib.types.path;
            default = null;
            example = "/run/keys/almagest-protected.hujson";
            description = ''
              Path to a protected HuJSON or JSON configuration file. Use this
              option for secrets that must not be stored in the Nix store. Set
              protectedSettings to null when this option is used.
            '';
          };

          extraArgs = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [];
            example = [ "--lvl" "debug" ];
            description = "Additional command-line arguments for almagestd.";
          };

          isolation = lib.mkOption {
            type = lib.types.enum [ "off" "auto" "strict" ];
            default = "off";
            example = "auto";
            description = ''
              Daemon process-isolation policy. The default is off because the
              current isolation implementation has performance costs. Auto
              and strict require a qualified Linux amd64 host.
            '';
          };
        };

        config = lib.mkIf cfg.enable {
          assertions = [
            {
              assertion = cfg.isolation == "off" || pkgs.stdenv.hostPlatform.system == "x86_64-linux";
              message = "services.almagestd.isolation supports auto and strict only on x86_64-linux.";
            }
            {
              assertion = (cfg.configFile == null) == (cfg.settings != null);
              message = "Set exactly one of services.almagestd.settings and services.almagestd.configFile.";
            }
            {
              assertion = cfg.initialSettings == null || cfg.configFile != null;
              message = "services.almagestd.initialSettings requires services.almagestd.configFile.";
            }
            {
              assertion = (cfg.protectedConfigFile == null) == (cfg.protectedSettings != null);
              message = "Set exactly one of services.almagestd.protectedSettings and services.almagestd.protectedConfigFile.";
            }
            {
              assertion = cfg.configFile != null || cfg.protectedConfigFile == null;
              message = "services.almagestd.protectedConfigFile requires services.almagestd.configFile.";
            }
          ];

          environment.systemPackages = [ cfg.package ];
          users.groups = {
            almagest = {};
            almagest-runtime = {};
            almagest-writer = {};
            almagest-command = {};
          };
          users.users = {
            almagest-runtime = {
              isSystemUser = true;
              group = "almagest-runtime";
              description = "Almagest isolated runtime";
            };
            almagest-writer = {
              isSystemUser = true;
              group = "almagest-writer";
              description = "Almagest isolated config writer";
            };
            almagest-command = {
              isSystemUser = true;
              group = "almagest-command";
              description = "Almagest isolated command helper";
            };
          };

          systemd.services.almagestd = {
            description = "Almagest network daemon";
            wantedBy = [ "multi-user.target" ];
            wants = [ "network-online.target" ];
            after = [ "network-online.target" ];
            preStart = lib.concatStringsSep "\n" (
              lib.optional (cfg.initialSettings != null) ''
                config_file=${lib.escapeShellArg (toString cfg.configFile)}
                if [ ! -e "$config_file" ]; then
                  ${pkgs.coreutils}/bin/install -D --mode=0600 ${initialConfig} "$config_file"
                fi
              ''
              ++ lib.optional (cfg.configFile != null && cfg.protectedSettings != null) ''
                ${pkgs.coreutils}/bin/install --mode=0600 ${generatedProtectedConfig} ${runtimeProtectedConfig}
              ''
              ++ lib.optional (cfg.isolation != "off") ''
                ${cfg.package}/libexec/almagest/almagest-isolation-storage prepare
              ''
            );
            serviceConfig = {
              Type = "simple";
              ExecStart =
                "${lib.getExe cfg.package} --isolation=${cfg.isolation}"
                + lib.optionalString (cfg.isolation == "off") " --file-storage /var/lib/almagest/store"
                + lib.optionalString useGeneratedConfig " --stdin"
                + lib.optionalString (!useGeneratedConfig) (
                  " --config ${lib.escapeShellArg (toString cfg.configFile)}"
                  + " --protected ${lib.escapeShellArg (toString protectedConfig)}"
                )
                + lib.optionalString (cfg.extraArgs != []) " ${lib.escapeShellArgs cfg.extraArgs}";
              StandardInput = lib.mkIf useGeneratedConfig "file:${generatedConfig}";
              Restart = "on-failure";
              RestartSec = "2s";
              UMask = "0077";
              RuntimeDirectory = "almagest";
              RuntimeDirectoryMode = "0755";
              StateDirectory = "almagest";
              StateDirectoryMode = "0755";
              StandardOutput = "journal";
              StandardError = "journal";
              SyslogIdentifier = "almagestd";
              Delegate = "cpu memory pids";
              TasksMax = "infinity";
              CapabilityBoundingSet = [
                "CAP_CHOWN"
                "CAP_DAC_OVERRIDE"
                "CAP_FOWNER"
                "CAP_KILL"
                "CAP_NET_ADMIN"
                "CAP_NET_BIND_SERVICE"
                "CAP_SETGID"
                "CAP_SETPCAP"
                "CAP_SETUID"
                "CAP_SYS_ADMIN"
              ];
              RestrictSUIDSGID = true;
              LockPersonality = true;
              RestrictRealtime = true;
              SystemCallArchitectures = "native";
            };
          };
        };
      };

      nixosModules.default = self.nixosModules.almagestd;
    };
}

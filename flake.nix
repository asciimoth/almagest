{
  description = "almagest dev env";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils = {
      url = "github:numtide/flake-utils";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    pre-commit-hooks = {
      url = "github:cachix/pre-commit-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = {
    self,
    nixpkgs,
    flake-utils,
    pre-commit-hooks,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };

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

      checks = {
        pre-commit-check = pre-commit-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
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
            govet.enable = true;
            gofmt.enable = true;
            golangci-lint.enable = true;
            gotidy = {
              enable = true;
              description = "Makes sure go.mod matches the source code";
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                just tidy
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            golangtest = {
              enable = true;
              description = "go test ./... --race";
              pass_filenames = false;
              entry = let script = pkgs.writeShellScript "gotesthook" ''
                just test-go
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            govulncheck = {
              enable = true;
              description = "Run govulncheck";
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
              files = "^(flake\\.nix$|justfile$|dev/winvm/|scripts/(lint-windows|test-e2e-platform|test-windows|test-windows-flow)\\.sh$)";
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
      '';
      wintunArchive = pkgs.fetchurl {
        url = "https://www.wintun.net/builds/wintun-0.14.1.zip";
        hash = "sha256-B8JWGF1u42UuCfpVwLZz4mJLVl4CxLkJHHnKfS8k71E=";
      };
      windowsWintun = pkgs.runCommand "almagest-wintun-0.14.1" {nativeBuildInputs = [pkgs.unzip];} ''
        unzip -q ${wintunArchive} -d unpacked
        install -Dm0444 unpacked/wintun/bin/amd64/wintun.dll $out/amd64/wintun.dll
        install -Dm0444 unpacked/wintun/bin/arm64/wintun.dll $out/arm64/wintun.dll
      '';
    in {
      packages = {
        windows-split-tunnel-drivers = windowsSplitTunnelDrivers;
        windows-wintun = windowsWintun;
        windows-runtime-dependencies = pkgs.symlinkJoin {
          name = "almagest-windows-runtime-dependencies";
          paths = [windowsSplitTunnelDrivers windowsWintun];
        };

        # Keep the old test-oriented output names for existing commands.
        windows-test-drivers = windowsSplitTunnelDrivers;
        windows-test-wintun = windowsWintun;
      };
      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        CHROME_BIN = testChromium;
        MULLVAD_SPLIT_TUNNEL_DRIVER_DIR = windowsSplitTunnelDrivers;
        SYSNET_WINDOWS_WINTUN_DIR = windowsWintun;
        PSSCRIPTANALYZER_PATH = "${psScriptAnalyzer}/share/powershell/Modules/PSScriptAnalyzer/PSScriptAnalyzer.psd1";
        WINVM_OVMF_CODE = pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.OVMF.fd}/FV/OVMF_CODE.fd";
        WINVM_OVMF_VARS = pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isLinux "${pkgs.OVMF.fd}/FV/OVMF_VARS.fd";
        buildInputs = with pkgs; [
          go
          golangci-lint
          govulncheck
          gopls

          typos
          commitizen
          shellcheck
          powershell
          ruff

          just
          python3
          jq

          nodejs_24
          pnpm
          typescript

          wireguard-tools
          xxd

          # for browser/e2e tests
          chromium

          tor
          obfs4
        ] ++ pkgs.lib.optionals (system == "x86_64-linux") [
          pkgs.qemu
          pkgs.xorriso
          pkgs.OVMF
        ];
      };
    });
}

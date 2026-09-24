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
          };
        };
      };

      testChromium = pkgs.writeShellScript "almagest-test-chromium" ''
        exec ${pkgs.chromium}/bin/chromium --password-store=basic "$@"
      '';
    in {
      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        CHROME_BIN = testChromium;
        buildInputs = with pkgs; [
          go
          golangci-lint
          govulncheck
          gopls

          typos
          commitizen
          shellcheck

          just

          nodejs_24
          pnpm
          typescript

          wireguard-tools
          xxd

          # for browser/e2e tests
          chromium
        ];
      };
    });
}

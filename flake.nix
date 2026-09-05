{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    # No nixpkgs-darwin-legacy pin: the project requires Go >= 1.25.5
    # (go.mod), and the -darwin stable branches (24.05, 24.11) only ship
    # Go 1.22.x / 1.23.x. nixpkgs-unstable still supports x86_64-darwin
    # (deprecation warning for 26.05, not removed yet), so a single
    # nixpkgs-unstable input covers all four target systems. Revisit when
    # nixpkgs-unstable drops x86_64-darwin and a -darwin branch with Go
    # 1.25+ exists.
  };

  outputs =
    { self
    , nixpkgs
    , ...
    }:
    let
      version = "2.4.0"; # x-release-please-version
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          treehouse = pkgs.buildGoModule {
            pname = "treehouse";
            inherit version;
            src = pkgs.lib.cleanSource ./.;
            vendorHash = "sha256-z8IndcHcZ6nLqhLtAYul3ppddpOA4AHGQWIlfYY/pfI=";
            ldflags = [
              "-X main.version=v${version}"
            ];
            # python3 is required by .github/scripts/no-mistakes-gate.sh,
            # which the test suite (TestNoMistakesGateDecisions) executes via
            # bash to parse pipeline attestation JSON. Without it the gate
            # falls through to the wrong error path and tests fail in the
            # Nix sandbox.
            nativeCheckInputs = [
              pkgs.git
              pkgs.python3
            ];
            # The cmd/ package's e2e tests (cmd/e2e_test.go) build the
            # treehouse binary, create git repos with bare remotes, and
            # spawn treehouse as a subprocess — all of which require network
            # access and unrestricted filesystem that the Nix sandbox does
            # not provide. The project's own CI (.github/workflows/ci.yml)
            # runs `go test ./...` on ubuntu, macOS, and Windows, which is
            # more comprehensive than the Nix sandbox can do. The Nix CI
            # workflow (.github/workflows/nix.yml) validates the binary with
            # `nix run .#default -- --version` instead.
            doCheck = false;
            meta = {
              description = "Git worktree pool manager for parallel AI coding agent workflows";
              homepage = "https://github.com/kunchenguid/treehouse";
              license = pkgs.lib.licenses.mit;
              mainProgram = "treehouse";
              platforms = systems;
            };
          };
        in
        {
          default = treehouse;
          treehouse = treehouse;
        }
      );

      apps = forAllSystems (
        system:
        {
          default = {
            type = "app";
            program = "${self.packages.${system}.default}/bin/${self.packages.${system}.default.meta.mainProgram}";
            meta = self.packages.${system}.default.meta;
          };
          treehouse = {
            type = "app";
            program = "${self.packages.${system}.treehouse}/bin/${self.packages.${system}.treehouse.meta.mainProgram}";
            meta = self.packages.${system}.treehouse.meta;
          };
        }
      );
    };
}

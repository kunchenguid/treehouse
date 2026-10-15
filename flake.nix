{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    # No x86_64-darwin: nixpkgs-unstable dropped it in 26.11 and throws on
    # evaluation.
    flake-compat.url = "github:edolstra/flake-compat";
    flake-compat.flake = false;
  };

  outputs =
    { self
    , nixpkgs
    , ...
    }:
    let
      version = "3.1.4"; # x-release-please-version
      systems = [
        "aarch64-darwin"
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
          treehouse = pkgs.callPackage ./package.nix { inherit version; };
        in
        {
          default = treehouse;
          inherit treehouse;
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

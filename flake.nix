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
      version = "2.3.0"; # x-release-please-version
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
          treehouse = import ./default.nix {
            inherit pkgs version;
          };
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

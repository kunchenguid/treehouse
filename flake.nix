{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs =
    { nixpkgs, ... }:
    let
      version = "1.3.2"; # x-release-please-version
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
        in
        {
          default = pkgs.buildGoModule {
            pname = "treehouse";
            inherit version;
            src = ./.;
            vendorHash = "sha256-fH93/19rZY/jduF4ZS0RLrqBWdCjz6XYnoN+3KPd4Lg=";
            ldflags = [
              "-X main.version=v${version}"
            ];
            nativeCheckInputs = [ pkgs.git ];
          };
        }
      );
    };
}

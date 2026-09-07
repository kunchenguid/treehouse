{ pkgs ? import <nixpkgs> {}
, version ? "2.3.0" # x-release-please-version
}:

pkgs.callPackage ./package.nix { inherit version; }

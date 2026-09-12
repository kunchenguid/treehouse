{ lib
, buildGoModule
, git
, python3
, version ? "2.3.0" # x-release-please-version
}:

buildGoModule {
  pname = "treehouse";
  inherit version;

  src = lib.cleanSource ./.;
  vendorHash = "sha256-z8IndcHcZ6nLqhLtAYul3ppddpOA4AHGQWIlfYY/pfI=";

  ldflags = [
    "-X main.version=v${version}"
  ];

  # python3 is required by .github/scripts/no-mistakes-gate.sh, which the
  # test suite (TestNoMistakesGateDecisions) executes via bash to parse
  # pipeline attestation JSON. Without it the gate falls through to the
  # wrong error path and tests fail in the Nix sandbox.
  nativeCheckInputs = [
    git
    python3
  ];

  # The cmd/ package's e2e tests build the treehouse binary, create git repos
  # with bare remotes, and spawn treehouse as a subprocess — all of which
  # require network access and unrestricted filesystem access that the Nix
  # sandbox does not provide. The project's own CI runs `go test ./...` on
  # ubuntu, macOS, and Windows, which is more comprehensive than the Nix
  # sandbox can do.
  doCheck = false;

  meta = {
    description = "Git worktree pool manager for parallel AI coding agent workflows";
    homepage = "https://github.com/kunchenguid/treehouse";
    license = lib.licenses.mit;
    mainProgram = "treehouse";
    platforms = [
      "aarch64-darwin"
      "x86_64-darwin"
      "aarch64-linux"
      "x86_64-linux"
    ];
  };
}

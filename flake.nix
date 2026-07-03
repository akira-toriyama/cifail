{
  # cifail — `nix run github:akira-toriyama/cifail` or `nix profile install`.
  #
  # vendorHash pins the vendored go modules; when go.mod/go.sum change, set it
  # back to pkgs.lib.fakeHash, run `nix build`, and paste the hash nix prints
  # ("got: sha256-...").
  description = "Bounded, high-signal extractor of a GitHub Actions run's failing logs (for agents)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        # Source/nix builds report a "dev" version identified by the commit;
        # tagged releases carry their real version via goreleaser (see
        # .goreleaser.yaml). Deriving the commit here avoids a hardcoded, stale
        # version string.
        version = "dev";
        rev = self.shortRev or self.dirtyShortRev or "unknown";
        v = "github.com/akira-toriyama/cifail/internal/version";
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "cifail";
          inherit version;
          src = ./.;
          vendorHash = "sha256-7K17JaXFsjf163g5PXCb5ng2gYdotnZ2IDKk8KFjNj0=";
          ldflags = [
            "-s" "-w"
            "-X ${v}.Version=${version}"
            "-X ${v}.Commit=${rev}"
          ];
          subPackages = [ "cmd/cifail" ];
          meta = with pkgs.lib; {
            description = "Bounded, high-signal extractor of a GitHub Actions run's failing logs";
            homepage = "https://github.com/akira-toriyama/cifail";
            license = licenses.mit;
            mainProgram = "cifail";
          };
        };

        apps.default = flake-utils.lib.mkApp {
          drv = self.packages.${system}.default;
          name = "cifail";
        };

        devShells.default = pkgs.mkShell {
          # go (not a pinned go_1_xx): nixpkgs removed EOL go versions; go.mod's
          # floor is satisfied by any current toolchain (GOTOOLCHAIN=local).
          packages = [ pkgs.go pkgs.golangci-lint pkgs.goreleaser pkgs.git-cliff pkgs.govulncheck ];
        };
      });
}

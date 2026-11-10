{
  description = "berth: daemonless resource leases and budgeted jobs";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-compat = {
      url = "github:edolstra/flake-compat";
      flake = false;
    };
  };

  outputs = { nixpkgs, ... }:
    let
      version = (builtins.fromJSON (builtins.readFile ./.release-please-manifest.json)).".";
      # nixpkgs-unstable no longer supports x86_64-darwin.
      forAllSystems = nixpkgs.lib.genAttrs [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
    in {
      packages = forAllSystems (system:
        let
          berth = nixpkgs.legacyPackages.${system}.callPackage ./package.nix { inherit version; };
        in {
          inherit berth;
          default = berth;
        });
      devShells = forAllSystems (system:
        let pkgs = nixpkgs.legacyPackages.${system};
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.just pkgs.goreleaser ];
          };
        });
    };
}

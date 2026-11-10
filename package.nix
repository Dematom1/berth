{ lib, buildGoModule, version ? (builtins.fromJSON (builtins.readFile ./.release-please-manifest.json))."." }:

buildGoModule {
  pname = "berth";
  inherit version;
  src = lib.cleanSource ./.;
  vendorHash = "sha256-0vsi8q/+H7I5qvyr9sIZFLD30yZivySLWYT6KrJqIcc=";
  subPackages = [ "." ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" "-X main.version=${version}" ];

  # Job/SSH tests spawn /bin/sh and other host tools absent from the sandbox.
  # CI runs the complete race suite on Linux and macOS instead.
  doCheck = false;

  meta = {
    description = "Daemonless resource leases and budgeted jobs";
    homepage = "https://github.com/Dematom1/berth";
    mainProgram = "berth";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}

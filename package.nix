{ lib, buildGoModule, version ? (builtins.fromJSON (builtins.readFile ./.release-please-manifest.json))."." }:

buildGoModule {
  pname = "dibs";
  inherit version;
  src = lib.cleanSource ./.;
  vendorHash = "sha256-9cmLdpULRzacfwiOrAGhvgcWHnJYEbIt9Mtb0BViU0w=";
  subPackages = [ "." ];
  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" "-X main.version=${version}" ];

  # Deprecated berth command: the same binary, warning on stderr when invoked as berth.
  postInstall = ''
    ln -s dibs $out/bin/berth
  '';

  # Job/SSH tests spawn /bin/sh and other host tools absent from the sandbox.
  # CI runs the complete race suite on Linux and macOS instead.
  doCheck = false;

  meta = {
    description = "Daemonless resource leases and budgeted jobs";
    homepage = "https://github.com/Dematom1/dibs";
    license = lib.licenses.asl20;
    mainProgram = "dibs";
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
}

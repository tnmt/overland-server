{ lib, buildGoModule }:
buildGoModule {
  pname = "overland-server";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
    ];
  };

  vendorHash = "sha256-yL7vt+5IJ/Qm2OJ2Jh7yTKvMECT9cEIZM5yeL0W29Z8=";

  env.CGO_ENABLED = "0";
  subPackages = [ "cmd/overland-server" ];
  ldflags = [
    "-s"
    "-w"
  ];

  meta = {
    description = "Self-hosted receiver for the Overland iOS location logger";
    mainProgram = "overland-server";
    license = lib.licenses.mit;
  };
}

{
  lib,
  buildGoModule,
}:
buildGoModule {
  pname = "media-organizer";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [../go.mod ../main.go ../internal];
  };

  vendorHash = null;
  ldflags = ["-s" "-w"];

  meta = {
    description = "Web app that files downloads into a movie and TV library";
    mainProgram = "media-organizer";
  };
}

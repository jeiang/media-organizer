{
  description = "Media organizer: file downloads into a movie and TV library, follow shows, stream in the browser";

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

  outputs = {
    self,
    nixpkgs,
  }: let
    systems = ["x86_64-linux" "aarch64-linux" "aarch64-darwin" "x86_64-darwin"];
    forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
  in {
    packages = forAll (pkgs: {
      default = pkgs.callPackage ./nix/package.nix {};
    });

    nixosModules.default = import ./nix/module.nix self;

    devShells = forAll (pkgs: {
      default = pkgs.mkShell {packages = [pkgs.go pkgs.gopls];};
    });

    checks = forAll (pkgs: {
      package = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    });

    formatter = forAll (pkgs: pkgs.alejandra);
  };
}

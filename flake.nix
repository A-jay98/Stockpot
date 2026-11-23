{
  description = "Stockpot: household kitchen inventory, shopping and recipes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      treefmt-nix,
      ...
    }:
    let
      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      treefmt = forAllSystems (
        pkgs:
        treefmt-nix.lib.evalModule pkgs {
          projectRootFile = "flake.nix";
          programs = {
            nixfmt.enable = true;
            gofumpt.enable = true;
            # web/ is formatted by its own pinned Biome (`make web-fmt`), so the
            # editor, CI and `nix fmt` never disagree on a Biome version.
          };
          settings.global.excludes = [
            "web/**"
            "*.gen.go"
          ];
        }
      );
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.go_1_26
            pkgs.golangci-lint
            pkgs.goose
            pkgs.sqlc
            pkgs.postgresql_17
            pkgs.nodejs_24
            pkgs.pnpm
            pkgs.gnumake # macOS ships make 3.81, which lacks .ONESHELL
            treefmt.${pkgs.stdenv.hostPlatform.system}.config.build.wrapper
          ];
        };
      });

      formatter = forAllSystems (pkgs: treefmt.${pkgs.stdenv.hostPlatform.system}.config.build.wrapper);

      checks = forAllSystems (pkgs: {
        formatting = treefmt.${pkgs.stdenv.hostPlatform.system}.config.build.check self;
        devShell = self.devShells.${pkgs.stdenv.hostPlatform.system}.default;
      });
    };
}

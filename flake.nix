{
  description = "llmdoc - LLM-optimized codebase documentation generator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    nixpkgs,
    flake-utils,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        packages.default = pkgs.buildGoModule {
          pname = "llmdoc";
          version = "0.1.5";
          src = ./.;
          vendorHash = "sha256-wWomchBSExia8EctDm/5do/q2Phk18oi46y/6J4jBY4=";

          nativeBuildInputs = with pkgs; [git];

          buildPhase = ''
            go build -o llmdoc .
          '';

          installPhase = ''
            install -D -m 755 llmdoc $out/bin/llmdoc
          '';

          meta = with pkgs.lib; {
            description = "LLM-optimized codebase documentation generator";
            homepage = "https://github.com/tristanmatthias/llmdoc";
            license = licenses.mit;
            maintainers = [
              {
                name = "ivy araliaceae";
                email = "me@ivy.gdn";
                github = "ivyara";
              }
            ];
          };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Go development
            go_1_25
            gotools
            gopls
            go-tools

            # Build tools
            gnumake
            git

            # Code quality
            goreleaser

            # Development utilities
            pkg-config
          ];

          shellHook = ''
            echo "llmdoc development environment loaded"
          '';
        };
      }
    );
}

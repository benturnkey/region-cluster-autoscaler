{
  description = "region-cluster-autoscaler";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            golangci-lint
            nodejs
            setup-envtest
            gh
          ];

          shellHook = ''
            export KUBEBUILDER_ASSETS="$(setup-envtest use 1.35.0 -p path)"
            if [ -f package-lock.json ] && [ ! -d node_modules/release-please ]; then
              echo "installing npm dependencies from package-lock.json"
              npm ci
            fi
          '';

        };
 });
}

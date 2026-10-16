{
  description = "dtctl — kubectl-inspired CLI for Dynatrace";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = {
    self,
    nixpkgs,
  }: let
    supportedSystems = [
      "x86_64-linux"
      "aarch64-linux"
      "x86_64-darwin"
      "aarch64-darwin"
    ];

    forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    pkgsFor = forAllSystems (system: import nixpkgs {inherit system;});

    # Read default version from pkg/version/version.go so the flake
    # tracks the in-tree value without a second source of truth.
    versionFile = builtins.readFile ./pkg/version/version.go;
    versionMatch = builtins.match ".*Version = \"([^\"]+)\".*" versionFile;
    defaultVersion =
      if versionMatch == null
      then "0.0.0"
      else builtins.head versionMatch;

    rev = self.rev or self.dirtyRev or "unknown";

    mkDtctl = pkgs: let
      relaxGoVersion = ''
        find . -name go.mod -exec ${pkgs.gnused}/bin/sed -i -E 's/^go [0-9.]+$/go ${pkgs.go.version}/' {} +
      '';
    in
      pkgs.buildGoModule {
        pname = "dtctl";
        version = defaultVersion;
        src = ./.;

        # nixpkgs can lag the Go patch release required by go.mod. Relax the `go`
        # directive in every module for the build.
        postPatch = relaxGoVersion;
        overrideModAttrs = _: {
          postPatch = relaxGoVersion;
        };

        # Cache module downloads instead of building a vendor tree, so the
        # hash does not change with the nixpkgs Go version.
        proxyVendor = true;

        subPackages = ["."];

        # Hash of the vendored Go modules. Regenerate with
        # `nix build .#dtctl --refresh` after bumping deps and update here.
        vendorHash = "sha256-tGz9UlhbgzP9CVfcW4KDVD5jyQ9MlSaxZXxU3+ZQ1Hs=";

        # Match the makefile's ldflags: embed version, commit, build date,
        # and strip debug info for a smaller binary.
        ldflags = [
          "-s"
          "-w"
          "-X github.com/dynatrace-oss/dtctl/pkg/version.Version=${defaultVersion}"
          "-X github.com/dynatrace-oss/dtctl/pkg/version.Commit=${rev}"
          "-X github.com/dynatrace-oss/dtctl/pkg/version.Date=1970-01-01T00:00:00Z"
        ];

        # Reproducible builds: no cgo, no VCS stamping by the Go toolchain.
        env = {
          CGO_ENABLED = "0";
          GOFLAGS = "-trimpath";
        };

        # Tests in this repo hit the real Dynatrace API or rely on network/keyring access.
        # `nix flake check` will still run `go vet` via buildGoModule.
        doCheck = false;

        meta = with pkgs.lib; {
          description = "kubectl-inspired CLI for Dynatrace (dashboards, workflows, SLOs, etc.)";
          homepage = "https://github.com/dynatrace-oss/dtctl";
          license = licenses.asl20;
          mainProgram = "dtctl";
          platforms = supportedSystems;
        };
      };
  in {
    packages = forAllSystems (system: let
      pkgs = pkgsFor.${system};
    in {
      default = mkDtctl pkgs;
      dtctl = mkDtctl pkgs;
    });

    apps = forAllSystems (system: {
      default = {
        type = "app";
        program = "${self.packages.${system}.default}/bin/dtctl";
        meta = self.packages.${system}.default.meta;
      };
    });

    devShells = forAllSystems (system: let
      pkgs = pkgsFor.${system};
    in {
      default = pkgs.mkShell {
        packages = with pkgs; [
          go_1_26
          gopls
          gotools # provides goimports
          golangci-lint
          govulncheck
          gnumake
          git
        ];

        # `go 1.26.5` in go.mod is newer than nixpkgs go_1_26 (1.26.2).
        # `auto` lets Go fetch the exact toolchain on first use; the local
        # cache lives under $GOPATH and is reused across shells.
        GOTOOLCHAIN = "auto";

        shellHook = ''
          export GOPATH="''${GOPATH:-$HOME/go}"
          export PATH="$GOPATH/bin:$PATH"
        '';
      };
    });

    formatter = forAllSystems (system: pkgsFor.${system}.alejandra);
  };
}

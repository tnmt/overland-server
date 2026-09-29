{
  config,
  lib,
  ...
}:
let
  cfg = config.services.overland-server;
in
{
  options.services.overland-server = {
    enable = lib.mkEnableOption "Overland location receiver";

    package = lib.mkOption {
      type = lib.types.package;
      description = "overland-server package to run.";
    };

    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:8095";
      description = "Address the HTTP server binds to. Put a TLS-terminating reverse proxy in front of it.";
    };

    ingestTokenFile = lib.mkOption {
      type = lib.types.path;
      description = ''
        File containing the token Overland sends as `access_token`.
        Read through systemd credentials, so it may be owned by root.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.overland-server = {
      description = "Overland location receiver";
      after = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        ExecStart = lib.escapeShellArgs [
          (lib.getExe cfg.package)
          "-listen"
          cfg.listenAddress
          "-db"
          "/var/lib/overland-server/overland.db"
          "-ingest-token-file"
          "%d/ingest-token"
        ];
        LoadCredential = [ "ingest-token:${cfg.ingestTokenFile}" ];
        DynamicUser = true;
        StateDirectory = "overland-server";
        StateDirectoryMode = "0700";
        Restart = "on-failure";
        RestartSec = 5;

        CapabilityBoundingSet = "";
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        NoNewPrivileges = true;
        PrivateDevices = true;
        PrivateTmp = true;
        ProtectClock = true;
        ProtectControlGroups = true;
        ProtectHome = true;
        ProtectHostname = true;
        ProtectKernelLogs = true;
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectSystem = "strict";
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
        ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = [ "@system-service" ];
        UMask = "0077";
      };
    };
  };
}

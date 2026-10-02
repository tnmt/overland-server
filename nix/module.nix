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

    readTokenFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        File containing the bearer token for the read API
        (`GET /api/days/{date}`). The read API is disabled when null.
      '';
    };

    writeTokenFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        File containing the bearer token for editing places
        (`POST /api/places`, `PATCH`/`DELETE /api/places/{id}`). It grants no
        read access and must differ from the read token. Editing is disabled
        when null.
      '';
    };

    timezone = lib.mkOption {
      type = lib.types.str;
      default = "Asia/Tokyo";
      description = "IANA time zone that defines calendar days for the read API.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.overland-server = {
      description = "Overland location receiver";
      after = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        ExecStart = lib.escapeShellArgs (
          [
            (lib.getExe cfg.package)
            "-listen"
            cfg.listenAddress
            "-db"
            "/var/lib/overland-server/overland.db"
            "-ingest-token-file"
            "%d/ingest-token"
            "-timezone"
            cfg.timezone
          ]
          ++ lib.optionals (cfg.readTokenFile != null) [
            "-read-token-file"
            "%d/read-token"
          ]
          ++ lib.optionals (cfg.writeTokenFile != null) [
            "-write-token-file"
            "%d/write-token"
          ]
        );
        LoadCredential = [
          "ingest-token:${cfg.ingestTokenFile}"
        ]
        ++ lib.optional (cfg.readTokenFile != null) "read-token:${cfg.readTokenFile}"
        ++ lib.optional (cfg.writeTokenFile != null) "write-token:${cfg.writeTokenFile}";
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

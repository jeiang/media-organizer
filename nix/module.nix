self: {
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.media-organizer;
  inherit (lib) mkOption types;
  under = dir: lib.hasPrefix "${cfg.mediaDir}/" dir;
in {
  options.services.media-organizer = {
    enable = lib.mkEnableOption "the media organizer web app";

    package = mkOption {
      type = types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "media-organizer.packages.\${system}.default";
      description = "The media-organizer package.";
    };

    listen = mkOption {
      type = types.str;
      default = "127.0.0.1:8090";
      description = "Address the web app listens on. It has no login; expose it only to trusted networks.";
    };

    user = mkOption {
      type = types.str;
      default = "media-organizer";
      description = "User the service runs as; created when left at the default.";
    };

    group = mkOption {
      type = types.str;
      default = "media";
      description = "Group that owns the library. The service writes group-writable files (umask 0002).";
    };

    mediaDir = mkOption {
      type = types.str;
      example = "/var/lib/media";
      description = ''
        Directory that holds every root below. It is the service's only writable path: hardlinks
        and moves fail across systemd bind mounts, so the roots cannot be separate ReadWritePaths.
      '';
    };

    moviesDir = mkOption {
      type = types.str;
      default = "${cfg.mediaDir}/movies";
      defaultText = lib.literalExpression ''"''${mediaDir}/movies"'';
      description = "Movie library: `Title (Year)/Title (Year).ext`.";
    };

    tvDir = mkOption {
      type = types.str;
      default = "${cfg.mediaDir}/tv";
      defaultText = lib.literalExpression ''"''${mediaDir}/tv"'';
      description = "TV library: `Show (Year)/Season NN/Show (Year) - SNNENN.ext`.";
    };

    downloadsDir = mkOption {
      type = types.str;
      default = "${cfg.mediaDir}/downloads";
      defaultText = lib.literalExpression ''"''${mediaDir}/downloads"'';
      description = "qBittorrent's save path, as qBittorrent reports it. Finished downloads are hardlinked from here.";
    };

    importDir = mkOption {
      type = types.nullOr types.str;
      default = "${cfg.mediaDir}/import";
      defaultText = lib.literalExpression ''"''${mediaDir}/import"'';
      description = "Folder whose entries are moved into the library; null disables imports.";
    };

    qbittorrent = {
      url = mkOption {
        type = types.nullOr types.str;
        default = null;
        example = "http://127.0.0.1:8081";
        description = "qBittorrent Web UI URL; null disables downloads and following.";
      };

      apiKeyFile = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "File holding the qBittorrent API key, read through a systemd credential.";
      };

      tag = mkOption {
        type = types.str;
        default = "media";
        description = "Tag that marks media torrents. Followed releases get it; only tagged torrents reach intake.";
      };
    };

    tmdbTokenFile = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "File holding a TMDB read access token or API key, for posters, overviews, and show search.";
    };

    eztvUrl = mkOption {
      type = types.str;
      default = "https://eztvx.to/api/get-torrents";
      description = "EZTV API endpoint used by followed shows.";
    };

    pollInterval = mkOption {
      type = types.str;
      default = "30m";
      description = "How often followed shows are checked, as a Go duration; \"0\" disables checks.";
    };

    removeSeeded = mkOption {
      type = types.bool;
      default = false;
      description = ''
        Delete filed torrents and their download copy once qBittorrent stops seeding them.
        The library keeps its hardlink, but the filing can no longer be undone.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = lib.all under ([cfg.moviesDir cfg.tvDir cfg.downloadsDir] ++ lib.optional (cfg.importDir != null) cfg.importDir);
        message = "services.media-organizer: every library root must be inside mediaDir (${cfg.mediaDir}).";
      }
      {
        assertion = cfg.qbittorrent.url == null || cfg.qbittorrent.apiKeyFile != null;
        message = "services.media-organizer: qbittorrent.url needs qbittorrent.apiKeyFile.";
      }
    ];

    users.users = lib.mkIf (cfg.user == "media-organizer") {
      media-organizer = {
        isSystemUser = true;
        inherit (cfg) group;
      };
    };
    users.groups.${cfg.group} = {};

    systemd.services.media-organizer = {
      description = "Media organizer";
      wantedBy = ["multi-user.target"];
      wants = ["network-online.target"];
      after = ["network-online.target"];
      serviceConfig = {
        ExecStart = lib.escapeShellArgs ([
            (lib.getExe cfg.package)
            "-listen"
            cfg.listen
            "-movies"
            cfg.moviesDir
            "-tv"
            cfg.tvDir
            "-downloads"
            cfg.downloadsDir
            "-tag"
            cfg.qbittorrent.tag
            "-eztv-url"
            cfg.eztvUrl
            "-poll-interval"
            cfg.pollInterval
            "-remove-seeded=${lib.boolToString cfg.removeSeeded}"
          ]
          ++ lib.optionals (cfg.importDir != null) ["-import" cfg.importDir]
          ++ lib.optionals (cfg.qbittorrent.url != null) [
            "-qbittorrent-url"
            cfg.qbittorrent.url
            "-qbittorrent-key-file"
            "%d/qbittorrent-api-key"
          ]
          ++ lib.optionals (cfg.tmdbTokenFile != null) ["-tmdb-token-file" "%d/tmdb-token"]);
        LoadCredential =
          lib.optional (cfg.qbittorrent.url != null) "qbittorrent-api-key:${cfg.qbittorrent.apiKeyFile}"
          ++ lib.optional (cfg.tmdbTokenFile != null) "tmdb-token:${cfg.tmdbTokenFile}";
        User = cfg.user;
        Group = cfg.group;
        StateDirectory = "media-organizer";
        StateDirectoryMode = "0700";
        CacheDirectory = "media-organizer";
        CacheDirectoryMode = "0700";
        UMask = "0002";
        Restart = "on-failure";
        RestartSec = "5s";

        ReadWritePaths = [cfg.mediaDir];
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        NoNewPrivileges = true;
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";
        ProtectClock = true;
        ProtectHostname = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectKernelLogs = true;
        ProtectControlGroups = true;
        ProtectProc = "invisible";
        ProcSubset = "pid";
        RestrictAddressFamilies = ["AF_INET" "AF_INET6" "AF_UNIX"];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = ["@system-service" "~@privileged" "~@resources"];
      };
    };
  };
}

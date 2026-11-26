# Media Organizer

A small web app for a home media library. It files finished downloads and
imports into a Plex/Jellyfin-style movie and TV library, follows shows and
sends new releases to qBittorrent, and streams the library to the browser.

- **Library**: poster wall of movies and shows (TMDB artwork when a token is
  set, generated posters otherwise), live search, and a page per title that
  plays files in the browser or downloads them.
- **Downloads**: add a magnet link or a `.torrent` file (v1 info hash required)
  and watch progress. Downloads added here or by a followed show are filed on
  their own when they finish, if the plan is exact: a known title, one video
  and a year for a movie, and every video numbered for a show the library
  already has (or with a year). Each is tried once; anything else waits in
  Intake with the reason, and automatic filings are undone like any other.
- **Intake**: finished qBittorrent downloads tagged `media`, plus every entry
  in the import folder. Each shows where its files will land; review to fix
  the title, year, kind, or episode numbers, then file. Downloads are
  hardlinked, so they keep seeding; imports are moved. Every filing can be
  undone, and undo refuses when it would delete the only copy of a file.
- **Following**: follow a show by IMDb ID (or pick it from TMDB search) with a
  release-name regex, previewed live against recent releases as you type. The first check marks existing releases as seen; later
  matches are added to qBittorrent with the `media` tag, and their non-video
  files are skipped.

Library layout:

```
movies/Title (Year)/Title (Year).mkv
movies/Title (Year)/Title (Year).en.srt
tv/Show (Year)/Season 01/Show (Year) - S01E01.mkv
```

It is one Go binary with no dependencies outside the standard library; pages
are server-rendered HTML with a little progressive JavaScript and a vendored
copy of [htmx](https://htmx.org) (2.0.11, 0BSD) for live regions. State (followed
shows, seen releases, filing receipts) is one JSON file in the state
directory. There is no login: serve it only on a trusted network.

## Run

```sh
nix develop
go run . -movies ~/media/movies -tv ~/media/tv -import ~/media/import
```

`go run . -h` lists every flag. qBittorrent integration needs `-qbittorrent-url`,
`-qbittorrent-key-file` (a Web UI API key), and `-downloads` (qBittorrent's save
path, on the same filesystem as the library so hardlinks work).

## NixOS

```nix
{
  inputs.media-organizer.url = "github:jeiang/media-organizer";

  # in a NixOS configuration
  imports = [inputs.media-organizer.nixosModules.default];
  services.media-organizer = {
    enable = true;
    listen = "100.64.0.1:8090";
    mediaDir = "/var/lib/media"; # holds movies/, tv/, downloads/, import/
    qbittorrent = {
      url = "http://127.0.0.1:8081";
      apiKeyFile = "/var/lib/qbittorrent/api-key";
    };
    tmdbTokenFile = "/run/secrets/tmdb-token";
  };
}
```

The service runs as `media-organizer` in group `media` with umask `0002`, and
can write only to `mediaDir`. qBittorrent must create files the group can
write (umask `0002`): Linux's `fs.protected_hardlinks` lets a user hardlink
only files it owns or can write.

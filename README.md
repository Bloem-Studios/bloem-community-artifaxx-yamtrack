# Silo Yamtrack Plugin

Scrobble-only Silo watch provider for a self-hosted [Yamtrack](https://github.com/FuzzyGrim/Yamtrack) instance.

Install the plugin, then paste the Jellyfin webhook URL from Yamtrack **Account settings → Integrations** under Silo **Settings → Watch Providers**.

This plugin talks to Yamtrack’s Jellyfin webhook. It is not a Floppy client. [Floppy](https://github.com/dannyvfilms/Floppy) is a Yamtrack fork with its own REST scrobble, history, and resume APIs; use the maintained [Floppy watch-provider plugin](https://github.com/Silo-Server/silo-plugin-watchprovider-floppy) for Floppy.

Silo reports:

- start → Yamtrack `Play` with `Played: false`
- pause → ignored (Yamtrack has no pause event)
- stop → `Stop` with `Played` taken from Silo’s `WatchSyncEvent.completed` flag
- incomplete stop → `Stop` with `Played: false`

Movies need TMDB or IMDb. Episodes need TVDB or IMDb. History import, progress sync, favorites, and watchlists are out of scope until Yamtrack has a stable write API for them.

## Dependency Model

This repository consumes `github.com/Silo-Server/silo-plugin-sdk` as a normal Go module dependency. CI and release builds run with `GOWORK=off` and expect the SDK version in `go.mod` to resolve from a published semver tag.

For local multi-repo development, use a temporary `replace` or a local `go.work` that points at a checkout of `silo-plugin-sdk`. Do not commit machine-local filesystem replaces as the supported release path.

## Development

Commands assume the repository root is the cwd.

```sh
go test ./...
go build -trimpath -ldflags="-s -w -X main.version=0.1.0" -o plugin .
./plugin manifest
```

Upload the binary through Silo’s admin plugin install flow.

## License

`silo-plugin-yamtrack` is licensed under `AGPL-3.0-or-later`. See [LICENSE](LICENSE).

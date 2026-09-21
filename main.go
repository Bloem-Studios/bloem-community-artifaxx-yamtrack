package main

import (
	_ "embed"

	pluginv1 "github.com/Bloem-Studios/bloem-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Bloem-Studios/bloem-plugin-sdk/pkg/pluginsdk/runtime"
)

// version is set at build time via -ldflags "-X main.version=...".
var version string

//go:embed manifest.json
var manifestJSON []byte

func main() {
	sdkruntime.ServeManifest(manifestJSON, version, sdkruntime.CapabilityServers{
		WatchSyncProvider: NewProvider(nil),
	})
}

// Ensure the generated unimplemented server stays embedded if methods are added.
var _ pluginv1.WatchSyncProviderServer = (*Provider)(nil)

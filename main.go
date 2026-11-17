package main

import (
	_ "embed"
	"os"

	"github.com/hashicorp/go-hclog"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"github.com/Silo-Server/silo-plugin-watchprovider-simkl/provider"
)

// version is set at build time with -ldflags "-X main.version=...".
var version string

//go:embed manifest.json
var manifestJSON []byte

func main() {
	server := provider.NewServer(nil)
	// Silo's plugin host re-logs each line of a plugin's stderr at the level
	// the line names, with its fields, when the line is in hclog's JSON format.
	// Plain text is re-logged at debug, below the host's level, and survives
	// only as an unleveled raw copy. The logger is made before Serve, which
	// swaps os.Stderr for a pipe the host does not parse.
	server.SetLogger(hclog.New(&hclog.LoggerOptions{
		Name:       "simkl",
		Level:      hclog.Info,
		Output:     os.Stderr,
		JSONFormat: true,
	}))
	runtime.ServeManifestWithOptions(manifestJSON, version, runtime.CapabilityServers{
		WatchSyncProvider: server,
	}, runtime.WithWatchSyncDeviceAuthorization(server.DeviceAuthorization()))
}

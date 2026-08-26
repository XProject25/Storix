// Package build carries compile-time metadata about the running Storix binary.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package build

import (
	"fmt"
	"runtime"
)

// Values are injected at link time by the Makefile:
//
//	-X github.com/XProject25/Storix/internal/build.Version=1.0.0
var (
	Version   = "dev"
	Commit    = "none"
	Date      = "unknown"
	Channel   = "stable"
	Developer = "X Project"
	Product   = "Storix"
	Repo      = "XProject25/Storix"
)

// UserAgent identifies Storix when it talks to the outside world.
func UserAgent() string {
	return fmt.Sprintf("%s/%s (+https://github.com/%s)", Product, Version, Repo)
}

// Platform reports the OS/arch pair the binary was built for.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Info is the machine readable build descriptor exposed through the API.
type Info struct {
	Product   string `json:"product"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	Channel   string `json:"channel"`
	Platform  string `json:"platform"`
	GoVersion string `json:"goVersion"`
	Developer string `json:"developer"`
}

// Current returns the build descriptor for this binary.
func Current() Info {
	return Info{
		Product:   Product,
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		Channel:   Channel,
		Platform:  Platform(),
		GoVersion: runtime.Version(),
		Developer: Developer,
	}
}

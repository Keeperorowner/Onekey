package steamtools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onekey/internal/models"
)

// Setup generates the SteamTools Lua unlock file in Steam's stplug-in directory.
//
// Lua semantics (matching the community manifest repos' reference .lua files):
//   addappid(<appid>)                       — declare ownership of the app
//   addappid(<depotid>, 0, "<decrkey>")     — inject a depot decryption key
//   setManifestid(<depotid>, "<manifestid>") — pin the manifest version
//
// NOTE: the second argument to addappid for depots is 0 (decryption key
// injection), NOT 1. Using 1 registers the value as an app access token,
// which prevents Steam from decrypting depot content ("content encrypted").
func Setup(steamPath string, appInfo *models.SteamAppInfo, manifests []models.ManifestInfo) error {
	stPath := filepath.Join(steamPath, "config", "stplug-in")
	if err := os.MkdirAll(stPath, 0755); err != nil {
		return fmt.Errorf("create stplug-in directory: %w", err)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "-- Generated Lua Manifest by Onekey\n")
	fmt.Fprintf(&b, "-- Steam App %s Manifest\n", appInfo.AppID)
	fmt.Fprintf(&b, "-- Name: %s\n", appInfo.Name)
	fmt.Fprintf(&b, "-- Generated: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "-- Total Depots: %d\n", appInfo.DepotCount)
	fmt.Fprintf(&b, "-- Total DLCs: %d\n", appInfo.DLCCount)
	fmt.Fprintf(&b, "\n-- Declare ownership of the app itself\n")
	fmt.Fprintf(&b, "addappid(%s)\n", appInfo.AppID)

	// If the app itself has an access token (workshop/protected app), register it.
	if appInfo.AccessToken != "" && appInfo.AccessToken != "0" && appInfo.AccessToken != "None" {
		fmt.Fprintf(&b, "addappid(%s, 1, \"%s\")\n", appInfo.AppID, appInfo.AccessToken)
	}

	fmt.Fprintf(&b, "\n-- Depot decryption keys + manifest pins\n")

	// Deduplicate by depot_id, keeping the first manifest seen per depot.
	seen := make(map[string]bool)
	for _, m := range manifests {
		if seen[m.DepotID] {
			continue
		}
		seen[m.DepotID] = true
		if m.DepotKey != "" && m.DepotKey != "None" {
			fmt.Fprintf(&b, "addappid(%s, 0, \"%s\")\n", m.DepotID, m.DepotKey)
		}
		if m.ManifestID != "" {
			fmt.Fprintf(&b, "setManifestid(%s,\"%s\")\n", m.DepotID, m.ManifestID)
		}
	}

	luaFile := filepath.Join(stPath, appInfo.AppID+".lua")
	if err := os.WriteFile(luaFile, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write lua file: %w", err)
	}

	return nil
}

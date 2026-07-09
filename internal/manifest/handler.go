package manifest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"onekey/internal/constants"
	"onekey/internal/httpclient"
	"onekey/internal/i18n"
	"onekey/internal/models"
)

var client = httpclient.Shared()

// ProgressFunc is called with a status message and current/total counts.
type ProgressFunc func(msg string, current, total int)

// Handler downloads and processes Steam manifest files.
type Handler struct {
	steamPath  string
	depotCache string
	cdnList    []string
	cdnMu      sync.Mutex
}

// NewHandler creates a manifest handler for the given Steam path and CDN list.
func NewHandler(steamPath string, cdnList []string) *Handler {
	cache := filepath.Join(steamPath, "depotcache")
	os.MkdirAll(cache, 0755)
	return &Handler{
		steamPath:  steamPath,
		depotCache: cache,
		cdnList:    cdnList,
	}
}

// ProcessManifests downloads and saves all manifests concurrently.
func (h *Handler) ProcessManifests(manifests []models.ManifestInfo, onProgress ProgressFunc) ([]models.ManifestInfo, error) {
	total := len(manifests)
	results := make([]models.ManifestInfo, total)
	success := make([]bool, total)

	sem := make(chan struct{}, 10) // max 10 concurrent
	var wg sync.WaitGroup
	var mu sync.Mutex
	current := 0

	for i, m := range manifests {
		wg.Add(1)
		go func(idx int, info models.ManifestInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ok := h.processSingle(info)

			mu.Lock()
			current++
			c := current
			if ok {
				results[idx] = info
				success[idx] = true
				if onProgress != nil {
					if h.manifestExists(info) {
						onProgress(i18n.T("manifest.status.exists", "depot_id", info.DepotID), c, total)
					} else {
						onProgress(i18n.T("manifest.status.downloaded", "depot_id", info.DepotID), c, total)
					}
				}
			} else {
				if onProgress != nil {
					onProgress(i18n.T("manifest.status.failed", "depot_id", info.DepotID), c, total)
				}
			}
			mu.Unlock()
		}(i, m)
	}

	wg.Wait()

	var processed []models.ManifestInfo
	for i, ok := range success {
		if ok {
			processed = append(processed, results[i])
		}
	}

	// Persist depot decryption keys to depotcache/config.vdf so Steam can
	// decrypt depot content even after a restart where SteamTools hasn't
	// loaded the Lua yet. This matches the legacy Python versions which
	// wrote DecryptionKey entries alongside the .manifest files.
	h.writeDepotKeysConfig(processed)

	return processed, nil
}

func (h *Handler) manifestPath(info models.ManifestInfo) string {
	return filepath.Join(h.depotCache, fmt.Sprintf("%s_%s.manifest", info.DepotID, info.ManifestID))
}

func (h *Handler) manifestExists(info models.ManifestInfo) bool {
	_, err := os.Stat(h.manifestPath(info))
	return err == nil
}

func (h *Handler) processSingle(info models.ManifestInfo) bool {
	if h.manifestExists(info) {
		return true
	}

	data := h.download(info)
	if data == nil {
		return false
	}

	payload := extractManifestPayload(data)
	h.removeOldManifests(info.DepotID, info.ManifestID)

	path := h.manifestPath(info)
	if err := os.WriteFile(path, payload, 0644); err != nil {
		return false
	}
	return true
}

func (h *Handler) download(info models.ManifestInfo) []byte {
	// GitHub-sourced manifests (URL shape "/{repo}/{sha}/{path}") use the
	// GitHub CDN templates instead of the Steam CDN list.
	if isGitHubPath(info.URL) {
		if data := downloadFromGitHubCDN(info.URL); data != nil {
			return data
		}
		return nil
	}
	for retry := 0; retry < 3; retry++ {
		h.cdnMu.Lock()
		cdns := make([]string, len(h.cdnList))
		copy(cdns, h.cdnList)
		h.cdnMu.Unlock()

		for i, cdn := range cdns {
			url := cdn + info.URL
			resp, err := client.Get(url)
			if err != nil {
				h.demoteCDN(i)
				continue
			}
			if resp.StatusCode == 200 {
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err == nil {
					return data
				}
			} else {
				resp.Body.Close()
				h.demoteCDN(i)
			}
		}
	}
	return nil
}

// isGitHubPath reports whether a URL is a GitHub manifest-repo path of the
// form "/{owner}/{repo}/{sha}/{path}" produced by FetchAppManifests.
func isGitHubPath(u string) bool {
	return strings.HasPrefix(u, "/")
}

// downloadFromGitHubCDN fetches a GitHub raw file by trying every GitHub CDN
// template against the path-style URL.
func downloadFromGitHubCDN(pathURL string) []byte {
	parts := strings.SplitN(strings.TrimPrefix(pathURL, "/"), "/", 4)
	if len(parts) < 4 {
		return nil
	}
	repo := parts[0] + "/" + parts[1]
	sha := parts[2]
	path := parts[3]
	for _, tmpl := range constants.GitHubCDNTemplates {
		url := strings.ReplaceAll(tmpl, "{repo}", repo)
		url = strings.ReplaceAll(url, "{sha}", sha)
		url = strings.ReplaceAll(url, "{path}", path)
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		if resp.StatusCode == 200 {
			data, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil {
				return data
			}
		} else {
			resp.Body.Close()
		}
	}
	return nil
}

// demoteCDN moves the CDN at index i to the end of the list so that
// subsequent downloads prefer other nodes first.
func (h *Handler) demoteCDN(i int) {
	h.cdnMu.Lock()
	defer h.cdnMu.Unlock()
	if i < 0 || i >= len(h.cdnList)-1 {
		return
	}
	bad := h.cdnList[i]
	h.cdnList = append(h.cdnList[:i], h.cdnList[i+1:]...)
	h.cdnList = append(h.cdnList, bad)
}

func (h *Handler) removeOldManifests(depotID, currentManifestID string) {
	entries, err := os.ReadDir(h.depotCache)
	if err != nil {
		return
	}
	prefix := depotID + "_"
	currentName := fmt.Sprintf("%s_%s.manifest", depotID, currentManifestID)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".manifest") && name != currentName {
			os.Remove(filepath.Join(h.depotCache, name))
		}
	}
}

func extractManifestPayload(content []byte) []byte {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return content
	}
	for _, f := range reader.File {
		if f.Name == "z" {
			rc, err := f.Open()
			if err != nil {
				return content
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return content
			}
			return data
		}
	}
	return content
}

// writeDepotKeysConfig merges depot decryption keys into
// <steamPath>/depotcache/config.vdf. Steam reads this file at startup to
// know how to decrypt depot content, so keys survive Steam restarts even
// if SteamTools hasn't injected the Lua yet. Existing keys for other depots
// are preserved; keys for the same depot are overwritten.
func (h *Handler) writeDepotKeysConfig(processed []models.ManifestInfo) {
	// Collect unique depot_id → key from processed manifests.
	keys := map[string]string{}
	for _, m := range processed {
		if m.DepotKey != "" && m.DepotKey != "None" {
			keys[m.DepotID] = m.DepotKey
		}
	}
	if len(keys) == 0 {
		return
	}

	configPath := filepath.Join(h.depotCache, "config.vdf")

	// Read existing config.vdf if present and parse existing depot keys.
	existingKeys := map[string]string{}
	if data, err := os.ReadFile(configPath); err == nil {
		existingKeys = parseDepotKeysFromConfigVDF(string(data))
	}

	// Merge: new keys overwrite existing ones for the same depot.
	for k, v := range keys {
		existingKeys[k] = v
	}

	// Write back as a simple VDF document.
	writeDepotKeysConfigVDF(configPath, existingKeys)
}

// parseDepotKeysFromConfigVDF extracts depot_id → DecryptionKey from a
// config.vdf blob using a minimal VDF token scan.
func parseDepotKeysFromConfigVDF(content string) map[string]string {
	keys := map[string]string{}
	tokens := tokenizeVDF(content)
	i := 0
	for i < len(tokens) {
		if tokens[i] == "depots" && i+1 < len(tokens) && tokens[i+1] == "{" {
			i += 2
			for i < len(tokens) && tokens[i] != "}" {
				depotID := tokens[i]
				if i+1 < len(tokens) && tokens[i+1] == "{" {
					i += 2
					for i < len(tokens) && tokens[i] != "}" {
						if tokens[i] == "DecryptionKey" && i+1 < len(tokens) {
							keys[depotID] = tokens[i+1]
							i += 2
							continue
						}
						i++
					}
					if i < len(tokens) && tokens[i] == "}" {
						i++
					}
					continue
				}
				i++
			}
			if i < len(tokens) && tokens[i] == "}" {
				i++
			}
			continue
		}
		i++
	}
	return keys
}

// writeDepotKeysConfigVDF writes depot keys as a config.vdf-format file.
func writeDepotKeysConfigVDF(path string, keys map[string]string) {
	var b strings.Builder
	b.WriteString("\"depots\"\n")
	b.WriteString("{\n")
	// Sort by numeric depot id for stable output.
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return atoiSafe(ids[i]) < atoiSafe(ids[j])
	})
	for _, id := range ids {
		b.WriteString(fmt.Sprintf("\t\"%s\"\n", id))
		b.WriteString("\t{\n")
		b.WriteString(fmt.Sprintf("\t\t\"DecryptionKey\"\t\"%s\"\n", keys[id]))
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	os.WriteFile(path, []byte(b.String()), 0644)
}

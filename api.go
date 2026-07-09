package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"onekey/internal/constants"
	"onekey/internal/httpclient"
	"onekey/internal/i18n"
	"onekey/internal/manifest"
	"onekey/internal/models"
)

var httpClient = httpclient.Shared()

// fetchKeyInfo returns key information without contacting the remote server.
// Key validation is skipped — a permanent key is returned directly,
// matching the muwenyan521/Onekey approach (跳过卡密验证，直接返回成功).
func fetchKeyInfo(key string) (*models.KeyInfo, error) {
	if key == "" {
		return nil, fmt.Errorf("%s", i18n.T("api.key_not_exist"))
	}
	return &models.KeyInfo{
		Key:        key,
		Type:       "permanent",
		IsActive:   true,
		UsageCount: 0,
		TotalUsage: 0,
	}, nil
}

func fetchAppData(apiKey, appID string, progress func(string)) (*models.SteamAppInfo, *models.SteamAppManifestInfo, error) {
	// Try the GitHub manifest-repo approach first (no key/server needed).
	if progress != nil {
		progress("查询 GitHub 清单仓库...")
	}
	if appInfo, manifestInfo, err := fetchAppDataFromGitHub(appID, progress); err == nil {
		return appInfo, manifestInfo, nil
	} else {
		if progress != nil {
			progress(fmt.Sprintf("GitHub 清单源未命中: %v，尝试服务器回退...", err))
		}
	}
	// Fall back to the legacy server backend if GitHub sources have no branch.
	return fetchAppDataFromServer(apiKey, appID)
}

// fetchAppDataFromGitHub retrieves game manifests and depot keys from the
// community GitHub manifest repositories (branch named after the App ID),
// restoring the original (pre-server) Onekey data source. Game name and DLC
// list come from the public Steam Store appdetails endpoint.
func fetchAppDataFromGitHub(appID string, progress func(string)) (*models.SteamAppInfo, *models.SteamAppManifestInfo, error) {
	if progress != nil {
		progress(fmt.Sprintf("搜索清单分支: App %s ...", appID))
	}
	mainM, dlcM, depotKeys, err := manifest.FetchAppManifests(appID, progress)
	if err != nil {
		return nil, nil, err
	}
	if progress != nil {
		progress(fmt.Sprintf("找到 %d 个清单文件，解析 depot 密钥...", len(mainM)+len(dlcM)))
	}

	// Game metadata from Steam Store (name, header image, DLC list).
	name := appID
	headerImage := ""
	dlcCount := 0
	var dlcIDs []int
	if data := fetchAppDetails(appID, ""); data != nil {
		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			if appData, ok := raw[appID].(map[string]any); ok {
				if d, ok := appData["data"].(map[string]any); ok {
					if n, ok := d["name"].(string); ok && n != "" {
						name = n
					}
					if img, ok := d["header_image"].(string); ok {
						headerImage = img
					}
					if dlcs, ok := d["dlc"].([]any); ok {
						dlcCount = len(dlcs)
						for _, v := range dlcs {
							if f, ok := v.(float64); ok {
								dlcIDs = append(dlcIDs, int(f))
							}
						}
					}
				}
			}
		}
	}

	appInfo := &models.SteamAppInfo{
		AppID:                 appID,
		Name:                  name,
		HeaderImage:           headerImage,
		DLCCount:              dlcCount,
		DepotCount:            len(mainM),
		WorkshopDecryptionKey: "None",
	}

	manifestInfo := &models.SteamAppManifestInfo{
		MainApp: mainM,
		DLCs:    dlcM,
	}

	// Append DLC depot manifests by fetching each DLC's own branch.
	for _, dlcID := range dlcIDs {
		dlcIDStr := fmt.Sprintf("%d", dlcID)
		if progress != nil {
			progress(fmt.Sprintf("获取 DLC %s 的清单...", dlcIDStr))
		}
		dMain, _, dKeys, derr := manifest.FetchAppManifests(dlcIDStr, nil)
		if derr != nil {
			continue
		}
		manifestInfo.DLCs = append(manifestInfo.DLCs, dMain...)
		for k, v := range dKeys {
			depotKeys[k] = v
		}
	}

	// Backfill any missing depot keys from the merged key set.
	for i := range manifestInfo.MainApp {
		if manifestInfo.MainApp[i].DepotKey == "" {
			if k, ok := depotKeys[manifestInfo.MainApp[i].DepotID]; ok {
				manifestInfo.MainApp[i].DepotKey = k
			}
		}
	}
	for i := range manifestInfo.DLCs {
		if manifestInfo.DLCs[i].DepotKey == "" {
			if k, ok := depotKeys[manifestInfo.DLCs[i].DepotID]; ok {
				manifestInfo.DLCs[i].DepotKey = k
			}
		}
	}

	// If the input app_id is itself a DLC (no main manifests, only DLC ones),
	// promote DLC manifests to main so they get processed correctly.
	if len(manifestInfo.MainApp) == 0 && len(manifestInfo.DLCs) > 0 {
		manifestInfo.MainApp = manifestInfo.DLCs
		manifestInfo.DLCs = nil
		appInfo.DepotCount = len(manifestInfo.MainApp)
	}

	return appInfo, manifestInfo, nil
}

// fetchAppDataFromServer is the legacy backend-based implementation kept as a
// fallback when no GitHub manifest branch exists for an app.
func fetchAppDataFromServer(apiKey, appID string) (*models.SteamAppInfo, *models.SteamAppManifestInfo, error) {
	appIDInt, err := strconv.Atoi(appID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", i18n.T("web.invalid_appid"))
	}

	reqBody, _ := json.Marshal(map[string]any{
		"app_id": appIDInt,
	})

	req, err := http.NewRequest("POST", constants.SteamAPIBase+"/getGame", bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s", i18n.T("error.network", "error", err.Error()))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("%s", i18n.T("error.invalid_json"))
	}

	if resp.StatusCode != 200 {
		msg := getStringField(raw, "msg")
		if msg == "" {
			msg = getStringField(raw, "message")
		}
		if msg == "" {
			msg = i18n.T("error.unknown")
		}
		return nil, nil, fmt.Errorf("%s", i18n.T("error.api_response", "error", msg))
	}

	if code, ok := raw["code"].(float64); ok && int(code) != 200 {
		msg := getStringField(raw, "msg")
		return nil, nil, fmt.Errorf("%s", i18n.T("error.server_response", "error", msg))
	}

	// App info is in root-level "app" object
	appData, ok := raw["app"].(map[string]any)
	if !ok || appData == nil {
		return nil, nil, fmt.Errorf("%s", i18n.T("error.no_game_data"))
	}

	appInfo := &models.SteamAppInfo{
		AppID:                 fmt.Sprintf("%d", getIntField(appData, "appid", 0)),
		Name:                  getStringField(appData, "name"),
		HeaderImage:           getStringField(appData, "image"),
		AccessToken:           getStringField(appData, "token"),
		DLCCount:              getIntField(appData, "dlcCount", 0),
		DepotCount:            0,
		WorkshopDecryptionKey: getStringField(appData, "workshopKey"),
	}
	if appInfo.WorkshopDecryptionKey == "" {
		appInfo.WorkshopDecryptionKey = "None"
	}

	manifestInfo := &models.SteamAppManifestInfo{}

	// Game depots are at root level "gameDepots"
	if gameDepots, ok := raw["gameDepots"].([]any); ok {
		appInfo.DepotCount = len(gameDepots)
		for _, item := range gameDepots {
			if m, ok := item.(map[string]any); ok {
				manifestInfo.MainApp = append(manifestInfo.MainApp, parseManifest(m))
			}
		}
	}

	// DLC depots are at root level "dlcDepots", grouped by DLC
	if dlcDepots, ok := raw["dlcDepots"].([]any); ok {
		for _, dlcItem := range dlcDepots {
			if dlcMap, ok := dlcItem.(map[string]any); ok {
				if manifests, ok := dlcMap["manifests"].([]any); ok {
					for _, item := range manifests {
						if m, ok := item.(map[string]any); ok {
							manifestInfo.DLCs = append(manifestInfo.DLCs, parseManifest(m))
						}
					}
				}
			}
		}
	}

	// When gameManifests is null but dlcManifests has content, the input app_id
	// is itself a DLC. Treat DLC manifests as main app manifests so they get
	// processed correctly (downloaded to depotcache and included in Lua config).
	if len(manifestInfo.MainApp) == 0 && len(manifestInfo.DLCs) > 0 {
		manifestInfo.MainApp = manifestInfo.DLCs
		manifestInfo.DLCs = nil
		appInfo.DepotCount = len(manifestInfo.MainApp)
	}

	return appInfo, manifestInfo, nil
}

func parseManifest(m map[string]any) models.ManifestInfo {
	return models.ManifestInfo{
		AppID:      intFieldStr(m, "app_id"),
		DepotID:    intFieldStr(m, "depot_id"),
		DepotKey:   getStringField(m, "depot_key"),
		ManifestID: getStringField(m, "manifest_id"),
		Size:       getStringField(m, "size"),
		URL:        getStringField(m, "url"),
	}
}

func getStringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// intFieldStr extracts a JSON number field as an integer string, avoiding
// scientific notation from float64 (e.g. "4.11013e+06" → "4110130").
func intFieldStr(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return fmt.Sprintf("%d", int64(n))
		case string:
			return n
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getIntField(m map[string]any, key string, defaultVal int) int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return defaultVal
}

func searchStore(term, lang, apiKey string) (*models.StoreSearchResult, error) {
	u := fmt.Sprintf("https://store.steampowered.com/api/storesearch/?term=%s&l=%s&cc=CN",
		url.QueryEscape(term), url.QueryEscape(lang))
	resp, err := httpClient.Get(u)
	if err == nil {
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var result models.StoreSearchResult
		if json.Unmarshal(data, &result) == nil && len(result.Items) > 0 {
			return &result, nil
		}
	}

	return searchStoreViaProxy(term, lang, apiKey)
}

func searchStoreViaProxy(term, lang, apiKey string) (*models.StoreSearchResult, error) {
	u := fmt.Sprintf("%s/steam/search?term=%s&l=%s",
		constants.SteamAPIBase, url.QueryEscape(term), url.QueryEscape(lang))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", i18n.T("error.network", "error", err.Error()))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var result models.StoreSearchResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%s", i18n.T("error.invalid_response", "error", err.Error()))
	}
	return &result, nil
}

// fetchParentApp queries Steam appdetails to check if appID is a DLC/music/etc.
// Returns (parentAppID, parentName) if it has a parent game, or ("", "") if not.
func fetchParentApp(appID, apiKey string) (string, string) {
	data := fetchAppDetails(appID, apiKey)
	if data == nil {
		return "", ""
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", ""
	}
	appData, ok := raw[appID].(map[string]any)
	if !ok {
		return "", ""
	}
	d, ok := appData["data"].(map[string]any)
	if !ok {
		return "", ""
	}
	// Any app with a "fullgame" field is a child (DLC, music, etc.)
	fg, ok := d["fullgame"].(map[string]any)
	if !ok {
		return "", ""
	}
	return getStringField(fg, "appid"), getStringField(fg, "name")
}

// fetchAppDetails tries Steam Store directly, falls back to backend proxy.
func fetchAppDetails(appID, apiKey string) []byte {
	u := fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%s", appID)
	resp, err := httpClient.Get(u)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			data, err := io.ReadAll(resp.Body)
			if err == nil && len(data) > 2 {
				return data
			}
		}
	}

	proxyURL := fmt.Sprintf("%s/steam/appdetails?appids=%s", constants.SteamAPIBase, appID)
	req, err := http.NewRequest(http.MethodGet, proxyURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-API-Key", apiKey)
	resp2, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp2.Body.Close()
	data, _ := io.ReadAll(resp2.Body)
	return data
}

func testProxyConnectivity(proxyURL string) (bool, string) {
	c := httpclient.Shared()
	old := c.Proxy()
	if err := c.SetProxy(proxyURL); err != nil {
		return false, i18n.T("settings.proxy_invalid")
	}
	defer c.SetProxy(old)

	resp, err := c.Get("https://store.steampowered.com/api/storesearch/?term=test&cc=CN&l=schinese&count=1")
	if err != nil {
		return false, i18n.T("settings.proxy_fail", "error", err.Error())
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, i18n.T("settings.proxy_fail", "error", fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	return true, i18n.T("settings.proxy_ok")
}

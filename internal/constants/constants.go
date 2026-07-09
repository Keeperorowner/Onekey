package constants

// SteamAPIBase is the base URL for the legacy game data API (no longer used for
// key/game data fetching; kept for announcements/version-update proxies only).
const SteamAPIBase = "https://ok.wwwweb.top/api"

// ManifestRepoList holds the GitHub repositories that store Steam depot
// manifests keyed by App-ID-named branches. Repos are checked in order and the
// one with the most recently updated branch wins. This mirrors the original
// (pre-server) Onekey data source approach.
//
// Order reflects maintenance activity & coverage (most active first) as of
// 2026-07. All entries verified to expose App-ID-named branches containing
// .manifest files plus a Key.vdf/key.vdf depot-decryption-key file.
var ManifestRepoList = []string{
	"tymolu233/ManifestAutoUpdate-fix", // most active (daily updates)
	"SSMGAlt/ManifestHub2",             // 328 stars, independent data set
	"steamtools-games/ManifestHub3",    // mirrors ManifestHub2 (same commits)
	"Auiowu/ManifestAutoUpdate",        // baseline, stable since 2026-02
}

// KeysRepoList holds GitHub repositories that store depot decryption keys in a
// keys/{appid}/config.vdf layout on the default branch. These repos do NOT
// contain .manifest files — they are used as a fallback to supply
// DecryptionKey entries when the manifest repos above have no Key.vdf or are
// missing keys for certain depots.
var KeysRepoList = []string{
	"Keeperorowner/SteamManifestKeys", // 102k+ decrypted depot key sets
}

// GitHubCDNTemplates are URL templates used to download raw files from a GitHub
// repo at a given commit sha. Placeholders: {repo} {sha} {path}. Tried in order
// until one succeeds (CN-friendly mirrors first).
var GitHubCDNTemplates = []string{
	"https://cdn.jsdmirror.com/gh/{repo}@{sha}/{path}",
	"https://raw.gitmirror.com/{repo}/{sha}/{path}",
	"https://raw.dgithub.xyz/{repo}/{sha}/{path}",
	"https://gh.akass.cn/{repo}/{sha}/{path}",
	"https://raw.githubusercontent.com/{repo}/{sha}/{path}",
}

// GitHubAPIBase is the base URL for the GitHub REST API.
const GitHubAPIBase = "https://api.github.com"

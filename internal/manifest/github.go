package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"onekey/internal/constants"
	"onekey/internal/httpclient"
	"onekey/internal/models"
)

var ghClient = httpclient.Shared()

// gitHubToken is used to authenticate GitHub API requests (raises rate limit
// from 60/hr to 5000/hr). Set via SetGitHubToken at startup.
var gitHubToken string

// SetGitHubToken configures the token used for authenticated GitHub API calls.
func SetGitHubToken(token string) {
	gitHubToken = strings.TrimSpace(token)
}

// ghGet performs an authenticated GET if a token is configured, otherwise a
// plain GET. Used for all GitHub API (api.github.com) calls.
func ghGet(url string) (*http.Response, error) {
	if gitHubToken != "" {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+gitHubToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		return ghClient.Do(req)
	}
	return ghClient.Get(url)
}

// GitHubBranch describes a manifest repo branch for an App ID.
type GitHubBranch struct {
	Repo string
	SHA  string
	Date time.Time
}

// githubBranchResponse mirrors the relevant fields of
// GET /repos/{owner}/{repo}/branches/{branch}.
type githubBranchResponse struct {
	Commit struct {
		SHA   string `json:"sha"`
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
			Tree struct {
				URL string `json:"url"`
			} `json:"tree"`
		} `json:"commit"`
	} `json:"commit"`
}

// githubTreeResponse mirrors GET /repos/{owner}/{repo}/git/trees/{sha}.
type githubTreeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

// FindManifestBranch queries every repo in ManifestRepoList for a branch named
// after appID and returns the most recently updated one.
func FindManifestBranch(appID string) (*GitHubBranch, error) {
	var best *GitHubBranch
	rateLimited := false
	for _, repo := range constants.ManifestRepoList {
		url := fmt.Sprintf("%s/repos/%s/branches/%s", constants.GitHubAPIBase, repo, appID)
		resp, err := ghGet(url)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 403 {
			rateLimited = true
			continue
		}
		if resp.StatusCode != 200 {
			continue
		}
		var br githubBranchResponse
		if err := json.Unmarshal(data, &br); err != nil {
			continue
		}
		if br.Commit.SHA == "" {
			continue
		}
		date, _ := time.Parse(time.RFC3339, br.Commit.Commit.Author.Date)
		b := &GitHubBranch{Repo: repo, SHA: br.Commit.SHA, Date: date}
		if best == nil || b.Date.After(best.Date) {
			best = b
		}
	}
	if best == nil {
		if rateLimited {
			return nil, fmt.Errorf("GitHub API 速率限制 (未认证每小时 60 次)，请稍后再试或配置 GitHub Token")
		}
		return nil, fmt.Errorf("所有清单仓库均未找到 App %s 的分支", appID)
	}
	return best, nil
}

// FetchAppManifests downloads the branch tree for the given app and returns the
// parsed manifest list (with GitHub raw URLs) plus depot decryption keys.
//
// Depots are returned grouped into "main" (depots whose id starts with the app
// id or are otherwise game depots) and "dlcs". In practice the legacy manifest
// repos do not separate DLC depots, so all depots are returned as main depots;
// the caller can still post-process them.
func FetchAppManifests(appID string, progress func(string)) (mainManifests, dlcManifests []models.ManifestInfo, depotKeys map[string]string, err error) {
	depotKeys = map[string]string{}

	if progress != nil {
		progress(fmt.Sprintf("在 %d 个清单仓库中查找 App %s 的分支...", len(constants.ManifestRepoList), appID))
	}
	branch, err := FindManifestBranch(appID)
	if err != nil {
		return nil, nil, nil, err
	}
	if progress != nil {
		progress(fmt.Sprintf("选中仓库: %s (更新于 %s)", branch.Repo, branch.Date.Format("2006-01-02")))
	}

	// Get the tree URL from the branch, then list the tree.
	branchURL := fmt.Sprintf("%s/repos/%s/branches/%s", constants.GitHubAPIBase, branch.Repo, appID)
	resp, err := ghGet(branchURL)
	if err != nil {
		return nil, nil, nil, err
	}
	bdata, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var br githubBranchResponse
	if err := json.Unmarshal(bdata, &br); err != nil {
		return nil, nil, nil, fmt.Errorf("parse branch: %w", err)
	}
	treeURL := br.Commit.Commit.Tree.URL
	if treeURL == "" {
		return nil, nil, nil, fmt.Errorf("no tree url for branch %s", appID)
	}

	tresp, err := ghGet(treeURL)
	if err != nil {
		return nil, nil, nil, err
	}
	tdata, _ := io.ReadAll(tresp.Body)
	tresp.Body.Close()
	var tree githubTreeResponse
	if err := json.Unmarshal(tdata, &tree); err != nil {
		return nil, nil, nil, fmt.Errorf("parse tree: %w", err)
	}

	for _, item := range tree.Tree {
		path := item.Path
		if path == "" {
			continue
		}
		lower := strings.ToLower(path)

		// Key.vdf → depot decryption keys
		if strings.HasSuffix(lower, "key.vdf") {
			if progress != nil {
				progress("下载 Key.vdf 解密密钥表...")
			}
			content, derr := downloadFromRepo(branch.Repo, branch.SHA, path)
			if derr != nil {
				continue
			}
			keys := ParseKeyVDF(content)
			for k, v := range keys {
				depotKeys[k] = v
			}
			if progress != nil {
				progress(fmt.Sprintf("解析到 %d 个 depot 密钥", len(keys)))
			}
			continue
		}

		// *.manifest → manifest file
		if strings.HasSuffix(lower, ".manifest") {
			depotID, manifestID, ok := parseManifestFilename(path)
			if !ok {
				continue
			}
			key := depotKeys[depotID]
			m := models.ManifestInfo{
				AppID:      appID,
				DepotID:    depotID,
				DepotKey:   key,
				ManifestID: manifestID,
				URL:        githubFileURL(branch.Repo, branch.SHA, path),
			}
			mainManifests = append(mainManifests, m)
		}
	}

	// Attach keys to manifests parsed before Key.vdf was read.
	for i := range mainManifests {
		if mainManifests[i].DepotKey == "" {
			if k, ok := depotKeys[mainManifests[i].DepotID]; ok {
				mainManifests[i].DepotKey = k
			}
		}
	}
	for i := range dlcManifests {
		if dlcManifests[i].DepotKey == "" {
			if k, ok := depotKeys[dlcManifests[i].DepotID]; ok {
				dlcManifests[i].DepotKey = k
			}
		}
	}

	// Fallback: if any manifests are still missing depot keys, try the
	// dedicated keys repos (Keeperorowner/SteamManifestKeys etc.) which
	// store keys/{appid}/config.vdf on the default branch.
	missingKeys := false
	for i := range mainManifests {
		if mainManifests[i].DepotKey == "" {
			missingKeys = true
			break
		}
	}
	if !missingKeys {
		for i := range dlcManifests {
			if dlcManifests[i].DepotKey == "" {
				missingKeys = true
				break
			}
		}
	}
	if missingKeys && len(constants.KeysRepoList) > 0 {
		extraKeys := FetchDepotKeysFromKeysRepo(appID, progress)
		for k, v := range extraKeys {
			depotKeys[k] = v
		}
		for i := range mainManifests {
			if mainManifests[i].DepotKey == "" {
				if k, ok := depotKeys[mainManifests[i].DepotID]; ok {
					mainManifests[i].DepotKey = k
				}
			}
		}
		for i := range dlcManifests {
			if dlcManifests[i].DepotKey == "" {
				if k, ok := depotKeys[dlcManifests[i].DepotID]; ok {
					dlcManifests[i].DepotKey = k
				}
			}
		}
	}

	if len(mainManifests) == 0 {
		return nil, nil, nil, fmt.Errorf("no manifest files found for app %s", appID)
	}
	return mainManifests, dlcManifests, depotKeys, nil
}

// downloadFromRepo fetches a raw file from the repo, trying each CDN template
// in order until one succeeds.
func downloadFromRepo(repo, sha, path string) ([]byte, error) {
	for _, tmpl := range constants.GitHubCDNTemplates {
		url := strings.ReplaceAll(tmpl, "{repo}", repo)
		url = strings.ReplaceAll(url, "{sha}", sha)
		url = strings.ReplaceAll(url, "{path}", path)
		resp, err := ghClient.Get(url)
		if err != nil {
			continue
		}
		if resp.StatusCode == 200 {
			data, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil {
				return data, nil
			}
		} else {
			resp.Body.Close()
		}
	}
	return nil, fmt.Errorf("download failed: %s/%s/%s", repo, sha, path)
}

// githubFileURL returns the first CDN URL that would be tried for a file. The
// manifest handler only needs a stable URL; it will itself retry CDNs, but the
// legacy repos serve via raw.githubusercontent so we embed that directly.
func githubFileURL(repo, sha, path string) string {
	// Build a path-style URL relative to a CDN host. The manifest handler
	// concatenates cdn + info.URL, so we return a path that works with the
	// raw.githubusercontent host included in the handler's cdn list via
	// a dedicated "github" CDN scheme. To keep the handler unchanged we
	// instead return an absolute URL and rely on downloadGithubManifest.
	return fmt.Sprintf("/%s/%s/%s", repo, sha, path)
}

var manifestNameRe = regexp.MustCompile(`^(\d+)_(\d+)\.manifest$`)

// parseManifestFilename extracts depotID and manifestID from a filename like
// "2347770_7138855853134977810.manifest".
func parseManifestFilename(name string) (depotID, manifestID string, ok bool) {
	m := manifestNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// --- VDF (Key.vdf) parser ---
//
// Key.vdf has the shape:
//   "depots"
//   {
//       "731"
//       {
//           "DecryptionKey" "bca9..."
//       }
//       ...
//   }
// We only need depot_id → DecryptionKey, so a minimal tokenizer is enough.

// ParseKeyVDF parses a Key.vdf blob and returns depot_id → decryption_key.
func ParseKeyVDF(content []byte) map[string]string {
	keys := map[string]string{}
	tokens := tokenizeVDF(string(content))
	// Walk tokens looking for "depots" { ... "<id>" { "DecryptionKey" "<key>" } }
	i := 0
	for i < len(tokens) {
		if tokens[i] == "depots" && i+1 < len(tokens) && tokens[i+1] == "{" {
			i += 2
			// inside depots block
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

// tokenizeVDF splits a VDF document into quoted-string tokens and bare tokens
// ({, }). Whitespace outside quotes is discarded.
func tokenizeVDF(s string) []string {
	var tokens []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == '"' {
				tokens = append(tokens, b.String())
				b.Reset()
				inQuote = false
			} else if c == '\\' && i+1 < len(s) {
				// escape sequence
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case '"':
					b.WriteByte('"')
				case '\\':
					b.WriteByte('\\')
				default:
					b.WriteByte(s[i])
				}
			} else {
				b.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '{', '}':
			if b.Len() > 0 {
				tokens = append(tokens, b.String())
				b.Reset()
			}
			tokens = append(tokens, string(c))
		case ' ', '\t', '\r', '\n':
			if b.Len() > 0 {
				tokens = append(tokens, b.String())
				b.Reset()
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '/' {
				// line comment
				for i < len(s) && s[i] != '\n' {
					i++
				}
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		tokens = append(tokens, b.String())
	}
	return tokens
}

// DepotKeysSorted returns depot ids sorted numerically for stable output.
func DepotKeysSorted(keys map[string]string) []string {
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		// numeric compare if possible, else lexical
		a, b := ids[i], ids[j]
		ai, bi := atoiSafe(a), atoiSafe(b)
		if ai != bi {
			return ai < bi
		}
		return a < b
	})
	return ids
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// FetchDepotKeysFromKeysRepo fetches depot decryption keys for the given appID
// from the KeysRepoList repos. Each repo stores keys at
// keys/{appid}/config.vdf on its default branch. This is used as a fallback
// when the manifest repos don't include a Key.vdf or are missing keys.
func FetchDepotKeysFromKeysRepo(appID string, progress func(string)) map[string]string {
	keys := map[string]string{}
	for _, repo := range constants.KeysRepoList {
		branchURL := fmt.Sprintf("%s/repos/%s/branches", constants.GitHubAPIBase, repo)
		resp, err := ghGet(branchURL)
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}

		var branches []githubBranchResponse
		if err := json.Unmarshal(data, &branches); err != nil || len(branches) == 0 {
			var single githubBranchResponse
			if err := json.Unmarshal(data, &single); err == nil && single.Commit.SHA != "" {
				branches = []githubBranchResponse{single}
			} else {
				continue
			}
		}

		sha := branches[0].Commit.SHA
		if sha == "" {
			continue
		}

		keyPath := fmt.Sprintf("keys/%s/config.vdf", appID)
		if progress != nil {
			progress(fmt.Sprintf("从密钥仓库 %s 获取 %s 的 depot 密钥...", repo, appID))
		}
		content, derr := downloadFromRepo(repo, sha, keyPath)
		if derr != nil {
			continue
		}

		parsed := ParseKeyVDF(content)
		for k, v := range parsed {
			keys[k] = v
		}
		if progress != nil && len(parsed) > 0 {
			progress(fmt.Sprintf("从密钥仓库解析到 %d 个 depot 密钥", len(parsed)))
		}
	}
	return keys
}

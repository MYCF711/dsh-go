// Package pluginmgr 的 registry 客户端：npm 兼容 packument + tarball 下载。
//
// 复用 llm 的重试策略（internal/llm/retry.go 的 4 次尝试 / base 500ms /
// 单次 cap 8s / 总预算 20s / 抖动 0.5）；该实现未导出，这里同参数重写，
// 不另造一套退避语义。
package pluginmgr

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	registryAttempts   = 4
	registryBaseWait   = 500 * time.Millisecond
	registryMaxWait    = 8 * time.Second
	registryBudget     = 20 * time.Second
	registryJitter     = 0.5
	registryMaxDoc     = 8 << 20
	registryMaxTarball = 200 << 20
)

// RegistryClient 是 npm 兼容 registry 的真实 HTTP 客户端。
type RegistryClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// ResolvedPackage 是一次 registry 解析结果。
type ResolvedPackage struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Tarball   string `json:"tarball"`
	Shasum    string `json:"shasum,omitempty"`
	Integrity string `json:"integrity,omitempty"`
}

// NewRegistryClient 构造客户端；base 为空返回 nil（调用方必须回结构化 refused）。
func NewRegistryClient(base, token string) *RegistryClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	return &RegistryClient{BaseURL: base, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type packument struct {
	Name     string            `json:"name"`
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball   string `json:"tarball"`
			Shasum    string `json:"shasum"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
		// Engines / PeerDependencies 是官方"每个版本补齐兼容性"用到的两个字段
		// （`versionFacts`）。用 RawMessage 而不是 map[string]string：registry 里
		// 一个包的 engines 写坏了不该让**整份** packument 解析失败（那样连版本名
		// 都读不出来），坏字段按"没有这个字段"处理。
		Engines          json.RawMessage `json:"engines"`
		PeerDependencies json.RawMessage `json:"peerDependencies"`
	} `json:"versions"`
}

// enginesOf 从 packument 的 `engines` 里取 `dsh` 范围。
//
// 只有真的写了 `engines.dsh` 才算"有引擎要求"：npm 包里 `engines.node` 很常见，
// 把整个 engines 对象翻成 Engines{} 会得到空 DSH，从而被
// compatibilityRequirements 判成"非法范围"，把兼容的版本误报成错误。
func enginesOf(raw json.RawMessage) *Engines {
	if len(raw) == 0 {
		return nil
	}
	var parsed struct {
		DSH string `json:"dsh"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	if strings.TrimSpace(parsed.DSH) == "" {
		return nil
	}
	return &Engines{DSH: parsed.DSH}
}

// peerDependenciesOf 取 packument 的 `peerDependencies`（没有就是 nil）。
func peerDependenciesOf(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed) == 0 {
		return nil
	}
	return parsed
}

// SplitRegistrySpec 把 `name[@range]` 拆成包名与 range（空 range = latest）。
func SplitRegistrySpec(spec string) (name, versionRange string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.Contains(spec, "://") {
		return "", "", fmt.Errorf("不是 registry 包规格: %q", spec)
	}
	if strings.HasPrefix(spec, "@") {
		slash := strings.Index(spec, "/")
		if slash < 0 {
			return "", "", fmt.Errorf("作用域包缺 /: %q", spec)
		}
		rest := spec[slash+1:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return spec[:slash+1+at], rest[at+1:], nil
		}
		return spec, "", nil
	}
	if at := strings.LastIndex(spec, "@"); at > 0 {
		return spec[:at], spec[at+1:], nil
	}
	return spec, "", nil
}

// Resolve 解析 name@range → 精确版本 + tarball；不存在的版本/包是真实错误。
func (r *RegistryClient) Resolve(ctx context.Context, spec string) (ResolvedPackage, error) {
	if r == nil {
		return ResolvedPackage{}, fmt.Errorf("没有配置 registry（DSHD_PLUGIN_REGISTRY）")
	}
	name, want, err := SplitRegistrySpec(spec)
	if err != nil {
		return ResolvedPackage{}, err
	}
	var doc packument
	endpoint := r.BaseURL + "/" + url.PathEscape(name)
	if err := r.getJSON(ctx, endpoint, &doc); err != nil {
		return ResolvedPackage{}, err
	}
	if doc.Name == "" {
		doc.Name = name
	}
	version := ""
	if want == "" || want == "*" || want == "latest" {
		version = doc.DistTags["latest"]
	} else if _, ok := doc.Versions[want]; ok {
		version = want
	} else {
		versions := make([]string, 0, len(doc.Versions))
		for v := range doc.Versions {
			if rangeSatisfies(v, want) {
				versions = append(versions, v)
			}
		}
		sort.Slice(versions, func(i, j int) bool { c, _ := CompareVersions(versions[i], versions[j]); return c > 0 })
		if len(versions) > 0 {
			version = versions[0]
		}
	}
	if version == "" {
		// no-matching-version 是官方认为"另一个源可能给不同答案"的失败之一
		// （索引可能过期），所以它必须带种类，而不是一段纯文本。
		return ResolvedPackage{}, &RegistryError{Kind: RegistryKindNoMatchingVersion,
			Err: fmt.Errorf("registry 没有 %s 满足 %q 的版本", name, want)}
	}
	entry, ok := doc.Versions[version]
	if !ok || entry.Dist.Tarball == "" {
		return ResolvedPackage{}, fmt.Errorf("registry %s@%s 没有 tarball", name, version)
	}
	return ResolvedPackage{Name: doc.Name, Version: version, Tarball: entry.Dist.Tarball,
		Shasum: entry.Dist.Shasum, Integrity: entry.Dist.Integrity}, nil
}

// Download 下载并解包 tarball 到 cacheDir/<name>/<version>；返回可交给 Manager
// 的本地目录。重复下载同版本幂等；解包做路径穿越/绝对路径/符号链接拒绝。
func (r *RegistryClient) Download(ctx context.Context, pkg ResolvedPackage, cacheDir string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("没有配置 registry")
	}
	if pkg.Tarball == "" || pkg.Version == "" || pkg.Name == "" {
		return "", fmt.Errorf("registry 解析结果不完整")
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, safeName(pkg.Name), pkg.Version)
	if manifest, err := os.Stat(filepath.Join(dst, "plugin.json")); err == nil && manifest.Mode().IsRegular() {
		return dst, nil
	}
	raw, err := r.download(ctx, pkg.Tarball)
	if err != nil {
		return "", err
	}
	if pkg.Shasum != "" {
		sum := sha1.Sum(raw)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), pkg.Shasum) {
			return "", fmt.Errorf("tarball sha1 不匹配：got %s want %s", hex.EncodeToString(sum[:]), pkg.Shasum)
		}
	}
	tmp, err := os.MkdirTemp(cacheDir, ".extract-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := extractTGZ(raw, tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		// 并发下载同一版本：另一个进程先 rename 成功则直接复用。
		if _, statErr := os.Stat(filepath.Join(dst, "plugin.json")); statErr == nil {
			return dst, nil
		}
		return "", err
	}
	return dst, nil
}

func (r *RegistryClient) getJSON(ctx context.Context, endpoint string, out any) error {
	raw, err := r.get(ctx, endpoint, registryMaxDoc)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("registry 响应不是合法 JSON: %w", err)
	}
	return nil
}

func (r *RegistryClient) download(ctx context.Context, endpoint string) ([]byte, error) {
	return r.get(ctx, endpoint, registryMaxTarball)
}

func (r *RegistryClient) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	var lastErr error
	start := time.Now()
	for attempt := 1; attempt <= registryAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if r.Token != "" {
			req.Header.Set("Authorization", "Bearer "+r.Token)
		}
		resp, err := r.HTTP.Do(req)
		if err != nil {
			lastErr = registryTransportError(err)
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			resp.Body.Close()
			if readErr != nil {
				lastErr = &RegistryError{Kind: RegistryKindNetwork, Err: readErr}
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if int64(len(body)) > limit {
					return nil, fmt.Errorf("registry 响应超过 %d 字节上限", limit)
				}
				return body, nil
			} else {
				statusErr := registryStatusError(resp.StatusCode,
					fmt.Errorf("registry %s 返回 HTTP %d: %s", endpoint, resp.StatusCode, truncateText(string(body), 300)))
				lastErr = statusErr
				if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
					return nil, lastErr
				}
				if wait := retryAfter(resp); wait > 0 {
					if time.Since(start)+wait > registryBudget {
						return nil, lastErr
					}
					time.Sleep(wait)
				}
			}
		}
		if attempt == registryAttempts || time.Since(start) >= registryBudget {
			break
		}
		wait := registryBaseWait << (attempt - 1)
		if wait > registryMaxWait {
			wait = registryMaxWait
		}
		wait = time.Duration(float64(wait) * (1 - registryJitter + float64(time.Now().UnixNano()%1000)/1000*registryJitter))
		if time.Since(start)+wait > registryBudget {
			break
		}
		time.Sleep(wait)
	}
	return nil, lastErr
}

func retryAfter(resp *http.Response) time.Duration {
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(raw + "s"); err == nil && seconds > 0 {
		if seconds > registryMaxWait {
			seconds = registryMaxWait
		}
		return seconds
	}
	return 0
}

func extractTGZ(raw []byte, dir string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("tarball gzip 无效: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := ""
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tarball 读取失败: %w", err)
		}
		name := filepath.ToSlash(header.Name)
		clean := strings.TrimPrefix(name, "./")
		if root == "" {
			root = strings.SplitN(clean, "/", 2)[0]
		}
		clean = strings.TrimPrefix(clean, root+"/")
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(clean))
		rel, err := filepath.Rel(dir, target)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("tarball 路径越界: %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("tarball 含链接 %q：拒绝（防越界）", header.Name)
		}
	}
	return nil
}

func safeName(name string) string {
	name = strings.TrimPrefix(name, "@")
	name = strings.ReplaceAll(name, "/", "__")
	if name == "" {
		return "package"
	}
	return name
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func rangeSatisfies(version, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" || want == "*" || want == "latest" {
		return true
	}
	if strings.HasPrefix(want, "^") || strings.HasPrefix(want, "~") {
		base := strings.TrimLeft(want, "^~")
		cmp, err := CompareVersions(version, base)
		if err != nil || cmp < 0 {
			return false
		}
		vp, _ := parseSemver(version)
		bp, _ := parseSemver(base)
		if strings.HasPrefix(want, "^") {
			return vp.core[0] == bp.core[0]
		}
		return vp.core[0] == bp.core[0] && vp.core[1] == bp.core[1]
	}
	for _, prefix := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(want, prefix) {
			cmp, err := CompareVersions(version, strings.TrimSpace(strings.TrimPrefix(want, prefix)))
			if err != nil {
				return false
			}
			switch prefix {
			case ">=":
				return cmp >= 0
			case "<=":
				return cmp <= 0
			case ">":
				return cmp > 0
			case "<":
				return cmp < 0
			default:
				return cmp == 0
			}
		}
	}
	cmp, err := CompareVersions(version, want)
	return err == nil && cmp == 0
}

// DownloadTarball 直接下载一个 tarball URL 并解包到 cacheDir/<sha1>/package；
// 用于官方 tarball 规格（不要求 registry packument）。
func DownloadTarball(ctx context.Context, rawURL, cacheDir string) (string, error) {
	client := &RegistryClient{HTTP: &http.Client{Timeout: 60 * time.Second}}
	raw, err := client.download(ctx, rawURL)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(raw)
	key := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheDir, "tarballs", key)
	if _, err := os.Stat(filepath.Join(dst, "plugin.json")); err == nil {
		return dst, nil
	}
	tmp, err := os.MkdirTemp(cacheDir, ".tarball-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := extractTGZ(raw, tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		if _, statErr := os.Stat(filepath.Join(dst, "plugin.json")); statErr == nil {
			return dst, nil
		}
		return "", err
	}
	return dst, nil
}

// CacheDir 返回本进程共享的 plugin 下载缓存目录；显式环境变量优先。
func CacheDir() string {
	if dir := strings.TrimSpace(os.Getenv("DSHD_PLUGIN_CACHE")); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "dsh-plugin-cache")
}

// RegistryFromEnv 按官方 options.registry 优先、环境变量兜底构造客户端。
func RegistryFromEnv(override *string) *RegistryClient {
	base := os.Getenv("DSHD_PLUGIN_REGISTRY")
	if override != nil && strings.TrimSpace(*override) != "" {
		base = *override
	}
	return NewRegistryClient(base, os.Getenv("DSHD_PLUGIN_REGISTRY_TOKEN"))
}

// PublishedVersion 是 packument 里的一行版本。
//
// Engines / PeerDependencies 是**该版本自己**的兼容性要求（不是已安装版本的），
// 官方 listVersions 会为每个版本补齐这一层（`versionFacts` + `evaluatePluginCompatibility`）；
// 宿主在本地做同一件事，因此不需要为每个版本再打一次网络请求。
type PublishedVersion struct {
	Version          string
	PublishedAt      string
	Tags             []string
	Prerelease       bool
	Deprecated       string
	Engines          *Engines
	PeerDependencies map[string]string
}

// Versions 读一个包的全部已发布版本（SemVer 降序）；tags 来自 dist-tags。
func (r *RegistryClient) Versions(ctx context.Context, name string) ([]PublishedVersion, error) {
	if r == nil {
		return nil, fmt.Errorf("没有配置 registry（DSHD_PLUGIN_REGISTRY）")
	}
	name, _, err := SplitRegistrySpec(name)
	if err != nil {
		return nil, err
	}
	var doc struct {
		DistTags map[string]string `json:"dist-tags"`
		Time     map[string]string `json:"time"`
		Versions map[string]struct {
			Version          string          `json:"version"`
			Deprecated       any             `json:"deprecated"`
			Engines          json.RawMessage `json:"engines"`
			PeerDependencies json.RawMessage `json:"peerDependencies"`
		} `json:"versions"`
	}
	if err := r.getJSON(ctx, r.BaseURL+"/"+url.PathEscape(name), &doc); err != nil {
		return nil, err
	}
	tagsByVersion := map[string][]string{}
	for tag, version := range doc.DistTags {
		tagsByVersion[version] = append(tagsByVersion[version], tag)
	}
	out := make([]PublishedVersion, 0, len(doc.Versions))
	for version, entry := range doc.Versions {
		if entry.Version != "" {
			version = entry.Version
		}
		deprecated := ""
		switch value := entry.Deprecated.(type) {
		case string:
			deprecated = value
		case bool:
			if value {
				deprecated = "true"
			}
		}
		out = append(out, PublishedVersion{Version: version, PublishedAt: doc.Time[version],
			Tags: tagsByVersion[version], Prerelease: strings.Contains(version, "-"), Deprecated: deprecated,
			Engines: enginesOf(entry.Engines), PeerDependencies: peerDependenciesOf(entry.PeerDependencies)})
	}
	sort.Slice(out, func(i, j int) bool { c, _ := CompareVersions(out[i].Version, out[j].Version); return c > 0 })
	return out, nil
}

// Base 返回配置的 registry 根（没有配置时为空）。
func (r *RegistryClient) Base() string {
	if r == nil {
		return ""
	}
	return r.BaseURL
}

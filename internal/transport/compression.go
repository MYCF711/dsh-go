package transport

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// HTTP 响应压缩：官方 `webserver.compressionLevel`（默认 1）与
// `webserver.compressionThresholdBytes`（默认 1024）。
//
// 为什么宿主原来没有：`internal/webfrontend` 只按 Accept-Encoding 从 dist 里挑
// 预先压好的 `.br`/`.gz`，那是**静态资源**的路径；`POST /api/<method>` 的 JSON
// 响应（长会话历史能到几百 KB）全程明文，官方这时是在运行期 gzip 的。
//
// 三个语义点必须和官方一致：
//   - 已知长度且 < 阈值 → 不压（避免为小响应多付一层 CPU，且小响应压了反而更大）；
//   - **未知长度 → 立即可压**（官方明确按"长度未知"处理，不等缓冲）；
//   - level = -1 等价官方 `null`（关闭压缩）：本仓用 -1 表示"没有压缩器"，
//     因为 gzip 的 0 档是"存而不压"，和"关掉"不是一回事。
const (
	// DefaultCompressionLevel 是官方 `webserver.compressionLevel` 的默认值。
	DefaultCompressionLevel = 1
	// DefaultCompressionThresholdBytes 是官方 `webserver.compressionThresholdBytes` 的默认值。
	DefaultCompressionThresholdBytes = 1024
	// EnvHTTPCompressionLevel 覆盖压缩级别（-1 = 关闭）。
	EnvHTTPCompressionLevel = "DSHD_HTTP_COMPRESSION_LEVEL"
	// EnvHTTPCompressionThresholdBytes 覆盖压缩阈值（字节）。
	EnvHTTPCompressionThresholdBytes = "DSHD_HTTP_COMPRESSION_THRESHOLD_BYTES"
)

// WithCompression 给 handler 套一层运行期 gzip。
//
//	level     < 0  → 原样返回 next（等价官方 tls/compression 的 null）
//	threshold < 0  → 用官方默认 1024
//
// 只压"客户端要 gzip 且没有自己压过"的响应；HEAD、1xx/204/304、WebSocket 升级
// 一律直通——这几种情况下包一层会让既有行为变形（尤其 Upgrade 需要 Hijack）。
func WithCompression(next http.Handler, level int, threshold int64) http.Handler {
	if level < 0 {
		return next
	}
	if threshold < 0 {
		threshold = DefaultCompressionThresholdBytes
	}
	return &compressionHandler{next: next, level: level, threshold: threshold}
}

type compressionHandler struct {
	next      http.Handler
	level     int
	threshold int64
}

func (h *compressionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// WebSocket 升级要 Hijack，包装器会把连接从 http.Server 手里拿走，
	// 所以 /ws 与带 Upgrade 头的请求一律直通（压缩对帧流也没有意义）。
	if r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		h.next.ServeHTTP(w, r)
		return
	}
	cw := &compressedResponse{ResponseWriter: w, level: h.level, threshold: h.threshold, status: http.StatusOK}
	h.next.ServeHTTP(cw, r)
	cw.finish()
}

// compressedResponse 延迟到"第一次写正文"才决定压不压：
// 阈值判据要 Content-Length，而那个头是 handler 在写体之前才设的。
type compressedResponse struct {
	http.ResponseWriter
	level     int
	threshold int64
	status    int
	decided   bool
	compress  bool
	gz        *gzip.Writer
}

func (w *compressedResponse) Header() http.Header { return w.ResponseWriter.Header() }

func (w *compressedResponse) WriteHeader(code int) {
	if w.decided {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	// 这些状态没有正文，压了会多出莫名其妙的 Content-Encoding。
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		w.decide(false)
		w.ResponseWriter.WriteHeader(code)
		return
	}
	// handler 自己已经压过（例如静态资源的预压缩路径）→ 不再二次压。
	if w.Header().Get("Content-Encoding") != "" {
		w.decide(false)
		w.ResponseWriter.WriteHeader(code)
		return
	}
	// 先记状态、不发头：真正的发头时机取决于接下来写的体的长度。
	w.status = code
}

func (w *compressedResponse) Write(p []byte) (int, error) {
	if !w.decided {
		length := int64(-1)
		if cl := w.Header().Get("Content-Length"); cl != "" {
			if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
				length = n
			}
		}
		switch {
		case w.Header().Get("Content-Encoding") != "":
			w.decide(false)
		case length >= 0 && length < w.threshold:
			// 已知长度且没到阈值：不压（小响应压了更大，还要多一次缓冲）。
			w.decide(false)
		default:
			w.decide(true)
			gz, err := gzip.NewWriterLevel(w.ResponseWriter, w.level)
			if err != nil {
				return 0, fmt.Errorf("transport: 初始化 gzip 失败: %w", err)
			}
			w.gz = gz
		}
	}
	if w.compress {
		return w.gz.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// decide 只生效一次：定下"压/不压"，然后才把状态头发出去。
func (w *compressedResponse) decide(compress bool) {
	if w.decided {
		return
	}
	w.decided = true
	w.compress = compress
	if compress {
		w.Header().Set("Content-Encoding", "gzip")
		// 压缩后长度变了，原 Content-Length 必须删掉，否则客户端按旧长度截断。
		w.Header().Del("Content-Length")
		// 同一 URL 的响应随 Accept-Encoding 变化，缓存必须分开存。
		w.Header().Add("Vary", "Accept-Encoding")
	}
	w.ResponseWriter.WriteHeader(w.status)
}

// finish 在 handler 返回后收尾：没写过体也要把状态头发出去（否则 200 会丢），
// 压缩流必须 Close 才会写出 gzip 尾巴（少了它客户端解压报 unexpected EOF）。
func (w *compressedResponse) finish() {
	if !w.decided {
		w.decide(false)
	}
	if w.gz != nil {
		_ = w.gz.Close()
	}
}

// Flush 让流式响应（长历史分页）在压缩下也能及时出门。
func (w *compressedResponse) Flush() {
	if w.gz != nil {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 把连接交还给调用方（WebSocket 之外的升级路径）。已经发了 gzip 头就
// 不能再 hijack：那时线路上已经有压缩字节，交出去只会得到一份坏连接。
func (w *compressedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.compress {
		return nil, nil, errors.New("transport: 响应已按 gzip 开始输出，不能再 hijack")
	}
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("transport: 底层 ResponseWriter 不支持 hijack")
	}
	w.decided = true
	return h.Hijack()
}

// Unwrap 让 http.ResponseController 能穿透到真实 writer（超时/丢连接控制用）。
func (w *compressedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// acceptsGzip 解析 Accept-Encoding：认 gzip 与 *，q=0 视为明确拒绝。
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		if q := qValue(params); q == 0 {
			continue
		}
		return true
	}
	return false
}

// qValue 取参数里的 q=，缺省按 1 处理（RFC 7231）。
func qValue(params string) float64 {
	for _, kv := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 1
		}
		return q
	}
	return 1
}

// compressionLevelFromEnv 解析压缩级别：默认 1（官方），-1 = 关闭，合法 0–9。
// 非法值退回默认（手滑的 env 不该让响应变成明文，也不该让进程起不来）。
func compressionLevelFromEnv() int {
	raw := strings.TrimSpace(envLookup(EnvHTTPCompressionLevel))
	if raw == "" {
		return DefaultCompressionLevel
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < -1 || n > 9 {
		return DefaultCompressionLevel
	}
	return n
}

// compressionThresholdFromEnv 解析压缩阈值：默认 1024（官方），0 = 已知长度也压。
func compressionThresholdFromEnv() int64 {
	raw := strings.TrimSpace(envLookup(EnvHTTPCompressionThresholdBytes))
	if raw == "" {
		return DefaultCompressionThresholdBytes
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return DefaultCompressionThresholdBytes
	}
	return n
}

// envLookup 是环境变量读取的唯一入口（测试可替换，避免 t.Setenv 的全局副作用
// 与并行测试打架）。
var envLookup = os.Getenv

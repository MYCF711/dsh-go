package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
)

// 官方两个列会话参数（`dsh-session-persistence-jsonl` 与
// `dsh-api-session-controller`）：
//
//   - `session.persistence.listConcurrency`：列会话时读日志文件的并发度，默认 3；
//   - `sessionController.listWorkSliceMs`：列会话的工作切片预算，默认 16ms —— 官方
//     这么切是为了不把事件循环一次占满，宿主这边对应的是"别让一次列会话长时间
//     占住调用方"，所以切片之间用 runtime.Gosched() 让出执行权。
//
// 为什么两个参数都落在 ListSessions 里：宿主只有这一条列会话读路径，参数挂在
// 别处就只是装饰。并发度决定"同时开几份日志"，切片预算决定"一次连续占用多久"。
const (
	// DefaultListConcurrency 是官方 `session.persistence.listConcurrency` 的默认值。
	DefaultListConcurrency = 3
	// DefaultListWorkSliceMS 是官方 `sessionController.listWorkSliceMs` 的默认值。
	DefaultListWorkSliceMS = 16
	// EnvListConcurrency 覆盖列会话并发度（1 = 退回串行）。
	EnvListConcurrency = "DSHD_LIST_CONCURRENCY"
	// EnvListWorkSliceMS 覆盖列会话单次工作切片预算（毫秒）。
	EnvListWorkSliceMS = "DSHD_LIST_WORK_SLICE_MS"
)

// ListOptions 是列会话的两个官方可调项。零值 = 用官方默认值（再叠加环境变量）。
type ListOptions struct {
	// Concurrency 是并发读取会话日志的路数；<=0 时取 EnvListConcurrency 或默认 3。
	Concurrency int
	// WorkSliceMS 是单个工作切片的毫秒预算；<=0 时取 EnvListWorkSliceMS 或默认 16。
	WorkSliceMS int
}

// listDescribe 是测试注入点：并发度与切片断言需要能放慢"读一份会话"这件事，
// 否则小语料上并发与切片都观察不到。生产路径永远是 describe。
var listDescribe = describe

// listSlices 记已完成的切片数（观测点；测试据此断言"真的切了"，不靠计时猜）。
var listSlices atomic.Int64

// Listing 是会话面板需要的一行。
type Listing struct {
	SessionID string    `json:"sessionId"`
	Cwd       string    `json:"cwd"`
	Preset    string    `json:"agentPreset,omitempty"`
	Parent    string    `json:"parentSession,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Summary 是从日志里摘出来的一句话：优先"最后一条助手消息"，没有再退到第一条用户消息。
	Summary string `json:"summary,omitempty"`
	// Title 非 nil 表示显式持久化的标题。指针保留"未设置"与显式空串的区别，
	// 值不做 trim/截断，session.fork 的逐字 title 因而能跨进程回到 session.list。
	Title       *string `json:"title,omitempty"`
	TitleSource string  `json:"titleSource,omitempty"`
	Events      int     `json:"events"`
	Created     int64   `json:"createdAt"`
	Readable    bool    `json:"readable"`
	// Error 非空表示这份会话读不动（损坏 / 版本不对），面板要能显示出来而不是假装没有。
	Error string `json:"error,omitempty"`
	// Copies > 1 表示同一个 session id 在多个项目目录下都有。
	// 换机器/换挂载点会让 projectKey 变，同一份会话就"住"到别的目录里去了；
	// 不合并的话面板上会出现两行一模一样的条目，按 id 删又会撞上"多处命中"。
	Copies int `json:"copies,omitempty"`
}

// ListSessions 扫 <home>/sessions 下的所有会话，返回按更新时间倒序的列表。
//
// cwd 非空时只列这个项目下的（用 projectKey 换算目录名，不扫全盘）。
//
// 每个会话都要**读整份日志**才能算摘要与事件数。这在真实语料上是 100MB 级别，
// 所以这里设了个上限：超过 maxScanBytes 的会话只读首 2MB 取摘要，事件数记为 -1。
func ListSessions(home, cwd string) ([]Listing, error) {
	return ListSessionsWithOptions(home, cwd, ListOptions{})
}

// ListSessionsWithOptions 是 ListSessions 的同一份实现，额外接受两个官方参数
// （并发度 / 工作切片预算）。结果与参数无关：并发只是让多份日志的读取重叠，
// 切片只是让出执行权，两者都不改变返回顺序与内容。
func ListSessionsWithOptions(home, cwd string, opts ListOptions) ([]Listing, error) {
	if home == "" {
		return nil, os.ErrInvalid
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = listConcurrencyFromEnv()
	}
	sliceMS := opts.WorkSliceMS
	if sliceMS <= 0 {
		sliceMS = listWorkSliceFromEnv()
	}
	sliceBudget := time.Duration(sliceMS) * time.Millisecond

	sessionsDir := filepath.Join(home, "sessions")
	var projectDirs []string
	if cwd != "" {
		projectDirs = []string{filepath.Join(sessionsDir, v4.ProjectKey(cwd))}
	} else {
		entries, err := os.ReadDir(sessionsDir)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				projectDirs = append(projectDirs, filepath.Join(sessionsDir, e.Name()))
			}
		}
	}

	// 先串行做目录枚举 + stat（便宜），再并行读日志（贵）。
	type listTarget struct {
		path string
		info os.FileInfo
	}
	targets := make([]listTarget, 0, 16)
	for _, dir := range projectDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name(), "session.v4.jsonl.zstd")
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			targets = append(targets, listTarget{path: path, info: info})
		}
	}

	// 结果按 target 下标落位，所以并发度与切片都不影响顺序（排序前的输入完全相同）。
	out := make([]Listing, len(targets))
	work := func(start, step int) {
		sliceStart := time.Now()
		for i := start; i < len(targets); i += step {
			out[i] = listDescribe(targets[i].path, targets[i].info)
			if sliceBudget > 0 && time.Since(sliceStart) >= sliceBudget {
				// 这一片已经用满预算：记一次切片、让出执行权、重开一片。
				listSlices.Add(1)
				runtime.Gosched()
				sliceStart = time.Now()
			}
		}
	}
	if concurrency <= 1 || len(targets) <= 1 {
		work(0, 1)
	} else {
		if concurrency > len(targets) {
			concurrency = len(targets)
		}
		var wg sync.WaitGroup
		wg.Add(concurrency)
		for i := 0; i < concurrency; i++ {
			go func(start int) {
				defer wg.Done()
				work(start, concurrency)
			}(i)
		}
		wg.Wait()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return dedupeByID(out), nil
}

// listConcurrencyFromEnv 解析列会话并发度（官方默认 3）。
func listConcurrencyFromEnv() int {
	return envPositiveInt(EnvListConcurrency, DefaultListConcurrency)
}

// listWorkSliceFromEnv 解析列会话工作切片预算（官方默认 16ms）。
func listWorkSliceFromEnv() int {
	return envPositiveInt(EnvListWorkSliceMS, DefaultListWorkSliceMS)
}

// envPositiveInt 读一个正整数环境变量；空/非法/非正一律退回默认值
// （参数是预算与并发度，退化成 0 会让列会话停摆，所以这里不接受"0 表示关闭"）。
func envPositiveInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// dedupeByID 把同一个 session id 的多份合并成一条。
//
// 保留**最新**的那份（按 UpdatedAt），并把份数写进 Copies。
// 不合并的后果实测过：不给 cwd 列会话时，同一份会话出现两行同样的 id——
// 面板上看不出区别，按 id 删除又会报「多处命中」。
func dedupeByID(list []Listing) []Listing {
	if len(list) < 2 {
		return list
	}
	// list 已按 UpdatedAt 倒序，所以第一次见到的就是最新的。
	seen := make(map[string]int, len(list))
	out := make([]Listing, 0, len(list))
	for _, it := range list {
		if i, ok := seen[it.SessionID]; ok {
			out[i].Copies++
			continue
		}
		seen[it.SessionID] = len(out)
		it.Copies = 1
		out = append(out, it)
	}
	for i := range out {
		if out[i].Copies == 1 {
			out[i].Copies = 0 // 只有一份时不打这个字段
		}
	}
	return out
}

const maxScanBytes = 2 << 20

func describe(path string, info os.FileInfo) Listing {
	l := Listing{UpdatedAt: info.ModTime(), Events: -1}
	// 只读首帧 + 尾部有界摘要：全量解码 183 个会话实测 4.37 秒。
	log, err := v4.ReadHeader(path)
	if err != nil || log.ID == "" {
		l.SessionID = strings.TrimSuffix(filepath.Base(filepath.Dir(path)), ".zstd")
		l.Readable = false
		l.Error = "读不了这份会话（格式或版本不对）"
		if info.Size() > maxScanBytes {
			l.Error = "会话太大，先跳过"
		}
		return l
	}
	l.SessionID = log.ID
	l.Cwd = log.Cwd
	l.Preset = log.AgentPreset
	l.Parent = log.ParentSession
	l.Created = log.CreatedAt
	l.Readable = true
	// 有界读尾部取摘要：全量解码 183 个会话要 4.37 秒，只读首帧 0.05 秒但没有标题。
	// 折中：header 给 id/cwd/时间，摘要从尾部有界往回找（最多 64 KB）。
	l.Events = -1
	if sum, serr := v4.ReadSummary(path, 256<<10, 80); serr == nil {
		l.Summary = sum
	}
	if title, source, present, terr := readExplicitTitle(path); terr != nil {
		l.Error = "会话 title 索引读不了: " + terr.Error()
	} else if present {
		l.Title = title
		l.TitleSource = source
	}
	return l
}

func readExplicitTitle(sessionPath string) (*string, string, bool, error) {
	current, present, err := readSessionMetadata(sessionPath, "session/title")
	if err != nil || !present {
		return nil, "", false, err
	}
	return current.Title, current.Source, true, nil
}

// summarize 取一句话摘要：最后一条助手消息优先，其次第一条用户消息。
func summarize(events []v4.Event) string {
	var firstUser, lastAssistant string
	for _, ev := range events {
		switch ev.Type {
		case "user/message":
			text := textOf(ev)
			if firstUser == "" && !isCompactCheckpoint(ev) {
				firstUser = text
			}
		case "assistant/message":
			if text := textOf(ev); text != "" {
				lastAssistant = text
			}
		}
	}
	s := lastAssistant
	if s == "" {
		s = firstUser
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

func isCompactCheckpoint(ev v4.Event) bool {
	var d struct {
		Source struct {
			Kind string `json:"kind"`
		} `json:"source"`
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return false
	}
	return d.Source.Kind == "compact-checkpoint"
}

func textOf(ev v4.Event) string {
	m, ok := messageOf(ev)
	if !ok {
		return ""
	}
	return m.Content
}

// messageOf 把一条事件里能喂给模型的那段文本取出来。
func messageOf(ev v4.Event) (llm.Message, bool) {
	if m, _, ok := surfaceMessage(ev); ok {
		return m, true
	}
	return llm.Message{}, false
}

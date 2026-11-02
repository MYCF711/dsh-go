package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dshgo/codecs/v4"
)

// 一条检查的结果。
type checkResult struct {
	Name   string // 检查项
	OK     bool
	Detail string // 实测到的东西（不是形容词）
	Next   string // 不 OK 时该做什么；OK 时为空
	Warn   bool   // 不致命，但值得看一眼
}

// doctorReport 是 dshd doctor 的输出。
type doctorReport struct {
	Home    string
	Results []checkResult
}

// Failed 表示有致命项。
func (r doctorReport) Failed() bool {
	for _, c := range r.Results {
		if !c.OK && !c.Warn {
			return true
		}
	}
	return false
}

// Render 打成人看的报告。
func (r doctorReport) Render(w io.Writer) {
	fmt.Fprintf(w, "dshd doctor · home=%s\n\n", r.Home)
	for _, c := range r.Results {
		// 警告优先于 ✓：结尾那行说的是"上面 ! 那行"，
		// 这一行就**必须**是 !，否则人对不上号。
		mark := "✓"
		switch {
		case c.Warn:
			mark = "!"
		case !c.OK:
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %-14s %s\n", mark, c.Name, c.Detail)
		if c.Next != "" {
			fmt.Fprintf(w, "    → %s\n", c.Next)
		}
	}
	fmt.Fprintln(w)
	if r.Failed() {
		fmt.Fprintln(w, "有致命问题：见上面 ✗ 那几行。")
		return
	}
	// 警告要出现在**最后一行**：只看结尾的人不能漏掉它。
	// 踩过：尾部半帧那项是 OK+Warn，原来结尾照样打"一切正常"。
	for _, c := range r.Results {
		if c.Warn {
			fmt.Fprintf(w, "能跑，但有 %d 项值得看一眼（上面 ! 那行）。"+string(rune(10)), countWarns(r))
			return
		}
	}
	fmt.Fprintln(w, "一切正常。")
}

// runDoctor 跑完整套检查。
//
// 设计原则：**每项都要有实测到的具体东西**（路径、数量、状态码），
// 而不是"正常/异常"。出问题时人最需要的是"到底哪里、什么值"。
func runDoctor(home, base string) doctorReport {
	rep := doctorReport{Home: home}
	rep.Results = append(rep.Results,
		checkHomeWritable(home),
		checkSessionsReadable(home),
		checkStaleLocks(home),
		checkGateway(base),
		checkManifest("dsh.toml"),
		checkRecentLog(home),
	)
	return rep
}

// 1. home 能不能写。
func checkHomeWritable(home string) checkResult {
	c := checkResult{Name: "home 可写"}
	st, err := os.Stat(home)
	if err != nil {
		if os.IsNotExist(err) {
			c.OK = true
			c.Detail = home + "（还不存在，第一次跑会自动建）"
			return c
		}
		c.Detail = err.Error()
		c.Next = "检查这个路径能不能访问"
		return c
	}
	if !st.IsDir() {
		c.Detail = home + " 是一个文件"
		c.Next = "换一个目录路径，或把那个文件挪走"
		return c
	}
	probe := filepath.Join(home, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		c.Detail = "写不进去: " + err.Error()
		c.Next = "确认目录权限；只读挂载/被占用的目录都会这样"
		return c
	}
	os.Remove(probe)
	c.OK = true
	c.Detail = "可以写"
	return c
}

// 2. 会话文件能不能读。
func checkSessionsReadable(home string) checkResult {
	c := checkResult{Name: "会话可读"}
	root := filepath.Join(home, "sessions")
	if _, err := os.Stat(root); err != nil {
		c.OK = true
		c.Detail = "还没有会话目录（第一次跑会建）"
		return c
	}
	var total, bad, torn int
	var badNames []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) != "session.v4.jsonl.zstd" {
			return nil
		}
		total++
		if total > 200 { // 抽样上限，doctor 不该扫一整天
			return nil
		}
		l, err := v4.ReadFile(p)
		if err != nil {
			bad++
			if len(badNames) < 3 {
				badNames = append(badNames, filepath.Base(filepath.Dir(p))+": "+err.Error())
			}
			return nil
		}
		if l.TailTruncatedAt > 0 {
			torn++
		}
		return nil
	})
	switch {
	case bad > 0:
		c.Detail = fmt.Sprintf("%d 份里 %d 份读不出来", total, bad)
		c.Next = "看这几份：" + strings.Join(badNames, "；") + "。读不出来的文件不会被删，但那段历史用不上"
	case torn > 0:
		c.OK = true
		c.Warn = true
		c.Detail = fmt.Sprintf("%d 份可读，其中 %d 份尾部有半帧（断电留下的）", total, torn)
		c.Next = "不用处理：前面的内容照常可用；`dshd session info --in <file>` 会提示截断位置"
	default:
		c.OK = true
		c.Detail = fmt.Sprintf("%d 份全部可读", total)
	}
	return c
}

// 3. 僵尸锁：锁文件在，但可能没人在写。
//
// 判断不了"那个进程还活着没"（Windows 上不可靠），所以只报警、
// 不报错——删不删由人决定，理由和路径都给出来。
func checkStaleLocks(home string) checkResult {
	c := checkResult{Name: "写锁"}
	root := filepath.Join(home, "sessions")
	var locks []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".lock" {
			return nil
		}
		locks = append(locks, p)
		return nil
	})
	if len(locks) == 0 {
		c.OK = true
		c.Detail = "没有写锁"
		return c
	}
	sort.Strings(locks)
	c.OK = true
	c.Warn = true
	c.Detail = fmt.Sprintf("%d 个锁文件", len(locks))
	shown := locks
	if len(shown) > 3 {
		shown = shown[:3]
	}
	c.Next = "如果确实没有别的 dshd 在写这些会话，删掉即可：" + strings.Join(shown, "；")
	return c
}

// 4. 模型网关能不能连上。
func checkGateway(base string) checkResult {
	c := checkResult{Name: "模型网关"}
	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimSuffix(base, "/") + "/models"
	resp, err := client.Get(url)
	if err != nil {
		c.Detail = "连不上 " + base
		c.Next = "确认网关在跑（本机是 magpie，端口 3425）；或换个 --base"
		return c
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Detail = fmt.Sprintf("%s 返回 %d", url, resp.StatusCode)
		c.Next = "401/403 检查 key；5xx 是网关自己的问题"
		return c
	}
	// 能解析出模型列表更好：说明这个端点真的是 OpenAI 兼容的
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(raw, &body)
	c.OK = true
	if len(body.Data) > 0 {
		c.Detail = fmt.Sprintf("连上了，%d 个模型（%s）", len(body.Data), body.Data[0].ID)
	} else {
		c.Detail = "连上了（没列出模型）"
	}
	return c
}

// 5. manifest 能不能读。
func checkManifest(path string) checkResult {
	c := checkResult{Name: "模块装配"}
	if _, err := os.Stat(path); err != nil {
		c.OK = true
		c.Detail = "没有 " + path + "（用内置默认模块）"
		return c
	}
	if _, err := loadMods(path, defaultHome(), 0, nil); err != nil {
		c.Detail = path + ": " + err.Error()
		c.Next = "manifest 语法或模块名写错了；`dshd modules list` 看能装配哪些"
		return c
	}
	c.OK = true
	c.Detail = path + " 读得通"
	return c
}

// cmdDoctor 是 `dshd doctor` 的入口。
func cmdDoctor(argv []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	home := fs.String("home", envOr("DSHD_HOME", "tmp/dshd-home"), "会话根目录")
	base := fs.String("base", "http://127.0.0.1:3425/v1", "OpenAI 兼容端点")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	rep := runDoctor(*home, *base)
	rep.Render(os.Stdout)
	if rep.Failed() {
		return fmt.Errorf("诊断发现致命问题")
	}
	return nil
}

// countWarns 数有几项是"能用但值得看一眼"。
func countWarns(r doctorReport) int {
	n := 0
	for _, c := range r.Results {
		if c.Warn {
			n++
		}
	}
	return n
}

// 6. 最近的日志里有没有错误——出事之后第一件事就是翻它。
func checkRecentLog(home string) checkResult {
	c := checkResult{Name: "运行日志"}
	dir := filepath.Join(home, "logs")
	// 先分清"还没有"和"写不了"——原来两种情况都说成"还没有日志"，
	// 而写不了恰恰是出事之后最需要知道的那件事。
	if st, statErr := os.Stat(dir); statErr == nil && !st.IsDir() {
		c.Detail = dir + " 是一个文件，不是目录"
		c.Next = "删掉它；在此之前 run/acp 都写不了日志，出事后没有东西可查"
		return c
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.OK = true
			c.Detail = "还没有日志（跑一次 run/acp 就会写）"
			return c
		}
		c.Detail = "日志目录读不了: " + err.Error()
		c.Next = "检查 " + dir + " 的权限"
		return c
	}
	if len(entries) == 0 {
		c.OK = true
		c.Detail = "还没有日志（跑一次 run/acp 就会写）"
		return c
	}
	var latest string
	var latestTime time.Time
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latest = filepath.Join(dir, e.Name())
		}
	}
	if latest == "" {
		c.OK = true
		c.Detail = "日志目录里没有 .log 文件"
		return c
	}
	// 数一下里面有多少条 WARN/ERROR，并把最后一条错误原文带出来
	raw, err := os.ReadFile(latest)
	if err != nil {
		c.OK = true
		c.Warn = true
		c.Detail = "最新日志读不出来: " + err.Error()
		c.Next = "检查文件权限: " + latest
		return c
	}
	var warns int
	var lastErr string
	for _, line := range strings.Split(string(raw), string(rune(10))) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.Level == "WARN" || rec.Level == "ERROR" {
			warns++
			if rec.Error != "" {
				lastErr = rec.Msg + ": " + rec.Error
			} else {
				lastErr = rec.Msg
			}
		}
	}
	c.OK = true
	if warns > 0 {
		c.Warn = true
		c.Detail = fmt.Sprintf("%s 里有 %d 条警告", filepath.Base(latest), warns)
		if lastErr != "" {
			c.Next = "最后一条: " + lastErr + "（完整内容见 " + latest + "）"
		} else {
			c.Next = "完整内容见 " + latest
		}
		return c
	}
	c.Detail = fmt.Sprintf("%s 里没有警告（%d 行）", filepath.Base(latest), strings.Count(string(raw), string(rune(10))))
	return c
}

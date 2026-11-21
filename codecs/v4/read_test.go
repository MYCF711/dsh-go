package v4

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadGoldenFile(t *testing.T) {
	l, err := ReadFile(filepath.Join("testdata", "small.v4.jsonl.zstd"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if l.Header.Type != "session" || l.Header.Version != 4 {
		t.Fatalf("header = %+v", l.Header)
	}
	if l.Header.ID == "" || l.Header.CreatedAt == 0 {
		t.Fatalf("header 缺字段: %+v", l.Header)
	}
	if l.Frames != 3 {
		t.Fatalf("帧数 = %d，期望 3（header 独立成帧 + 两个批次帧）", l.Frames)
	}
	if len(l.Events) != 4 {
		t.Fatalf("事件数 = %d，期望 4（header 之外的 4 行）", len(l.Events))
	}
	if l.Events[0].Type != "permission/preset" {
		t.Fatalf("首个事件 = %q", l.Events[0].Type)
	}
	for i, ev := range l.Events {
		if ev.Seq != int64(i) {
			t.Fatalf("第 %d 个事件的 seq = %d，期望 %d（seq 必须是文件内连续序号）", i, ev.Seq, i)
		}
		if len(ev.Raw) == 0 || !strings.HasPrefix(string(ev.Raw), "{") {
			t.Fatalf("第 %d 个事件没有保留原始行", i)
		}
	}
}

func TestHeaderRejectsExtraKeys(t *testing.T) {
	// 多一个键即非法：宽进会把 v5 的字段读成 v4。
	var h Header
	err := h.UnmarshalJSON([]byte(`{"type":"session","version":4,"id":"x","whatever":1}`))
	if err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("应拒绝白名单外的键，得到 %v", err)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	if _, err := Read([]byte("not zstd at all")); err == nil {
		t.Fatal("非 zstd 内容必须报错")
	}
	if _, err := Read(nil); err == nil {
		t.Fatal("空文件必须报错")
	}
}

func TestCorruptTailIsReported(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "small.v4.jsonl.zstd"))
	if err != nil {
		t.Fatal(err)
	}
	broken := b[:len(b)-6] // 砍掉尾部

	// 契约（2026-11-03 改）：尾部半帧**不能泼掉前面的**，但也**不能静默**。
	// 原来这里是"必须报错"——那样一次断电就整份历史读不出来。
	log, err := Read(broken)
	if err != nil {
		t.Fatalf("尾部截断不该让整份日志读不出来: %v", err)
	}
	if log.TailTruncatedAt == 0 {
		t.Fatal("截断必须报出来，不能静默少读几条")
	}
	if len(log.Events) == 0 {
		t.Fatal("前面的完整帧应该留着")
	}
}

// TestReadRealCorpus 是本包最有价值的一条测试：拿 $DSH_HOME 里的真实会话解析。
// CI 上没有这些文件，自动跳过。
//
// **必须取样，不许全量**（2026-11-21）。原先这里把 $DSH_HOME/sessions 下**所有**
// .zstd 全读一遍；本机那是 4944 个文件 / 3.6 GB，-race 下按 200 文件实测 25.9s
// 外推 ≈ 11~14 分钟——于是 codecs/v4 自己就顶掉了全套 `go test ./... -race` 的大部分
// 时间，机器上再有别的任务占着就会撞 30m 包超时：门禁"卡在 race 步"，后面 20 多道
// 守卫一条都跑不到（PR #104 因此漏掉 text-only 白名单漂移，靠 CI 才捞住）。
// 同包另一条真实语料测试本来就封了顶（TestRoundTripRealCorpus 12 个文件），
// internal/session 的三条分别是 3/15/40 个文件——只有这条没有上限。
//
// 取样**不是放宽断言**：取哪些文件是确定性的（按路径字典序等距取，同一个 home
// 每次一样，不会变成"有时快有时慢"的 flaky），取到的每一个都照样必须解析成功；
// 日志里报"语料总量 vs 本次读了多少 MB"让覆盖面可见。要全量深跑就显式开：
//
//	DSHGO_REAL_CORPUS_ALL=1 go test ./codecs/v4 -run TestReadRealCorpus
func TestReadRealCorpus(t *testing.T) {
	root := os.Getenv("DSH_HOME")
	if root == "" {
		root = `D:\DeepSeek Harness\home`
	}
	sessions := filepath.Join(root, "sessions")
	if _, err := os.Stat(sessions); err != nil {
		t.Skip("本机没有真实会话目录：", sessions)
	}

	// 取样上限。文件数与字节数双封顶，且单文件也有上限（本机最大的会话 79.7 MB，
	// 读它一个的代价就顶得上取样全部；跳过几个大文件要**报数**，不静默）。
	const (
		sampleFiles = 20
		sampleBytes = 24 << 20
		sampleOne   = 8 << 20
	)
	all := os.Getenv("DSHGO_REAL_CORPUS_ALL") == "1"

	var corpus []string
	var corpusBytes int64
	err := filepath.Walk(sessions, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".zstd") {
			return nil
		}
		corpus = append(corpus, p)
		corpusBytes += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus) == 0 {
		t.Skip("没有找到会话文件")
	}

	// 等距取样：walk 已按字典序，stride 取整后逐个取，覆盖到整棵语料树而不只是开头
	// 那几个会话目录（只读开头会在"某个工作区格式变了"时全体失明）。
	pick := corpus
	if !all {
		stride := (len(corpus) + sampleFiles - 1) / sampleFiles
		pick = make([]string, 0, sampleFiles)
		for i := 0; i < len(corpus) && len(pick) < sampleFiles; i += stride {
			pick = append(pick, corpus[i])
		}
	}

	var files, events, frames, skippedBig int
	var readBytes int64
	var failures []string
	for _, p := range pick {
		info, statErr := os.Stat(p)
		if statErr != nil {
			continue
		}
		if !all {
			if info.Size() > sampleOne {
				skippedBig++
				continue
			}
			if readBytes >= sampleBytes {
				break
			}
		}
		l, err := ReadFile(p)
		if err != nil {
			failures = append(failures, p+": "+err.Error())
			continue
		}
		files++
		readBytes += info.Size()
		events += len(l.Events)
		frames += l.Frames
	}
	t.Logf("真实语料：总量 %d 个文件 / %.1f MB；本次读 %d 个文件 / %.1f MB（跳过 >%d MB 的 %d 个），%d 帧，%d 事件（全量模式=%v）",
		len(corpus), float64(corpusBytes)/(1<<20), files, float64(readBytes)/(1<<20),
		sampleOne>>20, skippedBig, frames, events, all)
	if files == 0 {
		t.Skip("没有读到可用会话（取样全被单文件上限跳过？要全量就设 DSHGO_REAL_CORPUS_ALL=1）")
	}
	if len(failures) > 0 {
		t.Fatalf("%d/%d 个文件解析失败，前 5 条：\n%s", len(failures), files+len(failures), strings.Join(failures[:min(5, len(failures))], "\n"))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

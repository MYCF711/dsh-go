package main

// `dshd analytics` 是产品分析本地管道的用户入口：
//
//	dshd analytics policy [--home] [--enabled=true|false] [--mode=local|local+export]
//	                      [--collector=URL] [--retention-days=N] [--sampling-rate=0..1]
//	dshd analytics dump   [--home] [--since=RFC3339]
//	dshd analytics aggregate
//	dshd analytics verify
//	dshd analytics purge
//
// 这些命令只读/写 <home>/state/analytics/**；不启动服务、不发网络请求
// （policy 只落盘，export 由 report 在服务里按策略执行）。
//
// 接线：main.go 的 switch os.Args[1] 里加一行 `case "analytics": err = cmdAnalytics(os.Args[2:])`
// （见 WIRING.md；main.go 在共享树有他人未提交改动，按 WIRING 串行接入）。

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"dshgo/internal/analytics"
)

func cmdAnalytics(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: dshd analytics policy|dump|aggregate|verify|purge [--home <dir>]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("analytics "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	home := fs.String("home", defaultHome(), "会话根目录（默认 DSHD_HOME 或 ~/.dshd）")

	var enabled *bool
	var mode *string
	var collector *string
	var retentionDays *int
	var samplingRate *float64
	var since *string
	switch sub {
	case "policy":
		enabledFlag := fs.String("enabled", "", "true|false；省略则只读")
		modeFlag := fs.String("mode", "", "local|local+export；省略则不改")
		collectorFlag := fs.String("collector", "", "collector URL；传空串可清除")
		retentionFlag := fs.Int("retention-days", 0, "本地事件保留天数；省略则不改")
		samplingFlag := fs.Float64("sampling-rate", 0, "采样率 [0,1]；省略则不改")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		visited := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
		if visited["enabled"] {
			value := *enabledFlag == "true"
			if *enabledFlag != "true" && *enabledFlag != "false" {
				return fmt.Errorf("--enabled 只能是 true 或 false，收到 %q", *enabledFlag)
			}
			enabled = &value
		}
		if visited["mode"] {
			mode = modeFlag
		}
		if visited["collector"] {
			collector = collectorFlag
		}
		if visited["retention-days"] {
			retentionDays = retentionFlag
		}
		if visited["sampling-rate"] {
			samplingRate = samplingFlag
		}
	case "dump":
		sinceFlag := fs.String("since", "", "只显示 receivedAt 之后的记录（RFC3339）")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		since = sinceFlag
	case "aggregate", "verify", "purge":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知 analytics 子命令 %q（可用：policy/dump/aggregate/verify/purge）", sub)
	}

	service, err := analytics.Open(*home)
	if err != nil {
		return err
	}
	defer service.Close()

	switch sub {
	case "policy":
		if enabled != nil || mode != nil || collector != nil || retentionDays != nil || samplingRate != nil {
			next := service.Policy()
			if enabled != nil {
				next.Enabled = *enabled
			}
			if mode != nil {
				next.Mode = *mode
			}
			if collector != nil {
				next.CollectorURL = *collector
			}
			if retentionDays != nil {
				next.RetentionDays = *retentionDays
			}
			if samplingRate != nil {
				next.SamplingRate = *samplingRate
			}
			next, err = service.UpdatePolicy(next)
			if err != nil {
				return err
			}
			return writeAnalyticsJSON(next)
		}
		return writeAnalyticsJSON(service.Policy())

	case "dump":
		var sinceTime time.Time
		if since != nil && *since != "" {
			parsed, err := time.Parse(time.RFC3339, *since)
			if err != nil {
				return fmt.Errorf("--since 必须是 RFC3339: %w", err)
			}
			sinceTime = parsed
		}
		records, err := service.Dump(sinceTime)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		for _, record := range records {
			if err := encoder.Encode(record); err != nil {
				return err
			}
		}
		return nil

	case "aggregate":
		aggregate, err := service.Aggregate()
		if err != nil {
			return err
		}
		return writeAnalyticsJSON(aggregate)

	case "verify":
		count, err := service.Verify()
		if err != nil {
			return err
		}
		return writeAnalyticsJSON(map[string]any{"ok": true, "records": count})

	case "purge":
		count, err := service.Purge()
		if err != nil {
			return err
		}
		return writeAnalyticsJSON(map[string]any{"ok": true, "purged": count})
	}
	return nil
}

func writeAnalyticsJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

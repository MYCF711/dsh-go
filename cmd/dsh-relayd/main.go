// Command dsh-relayd 是自托管的 Liveshare relay。
//
// 它只做三件事：
//   - 用 owner token 认证本机 owner（token 首次启动生成，落 <home>/owner-token.json，0600）；
//   - 管理 share（创建/列出/停止/权限/能力）与真实审计（<home>/audit.jsonl）；
//   - 提供 viewer WebSocket 与 owner 发布 WebSocket 的数据面（/v1/connect /v1/publish）。
//
// 不依赖外部服务、不联网、不做 OIDC：viewer 身份来自 --viewers 配置的共享密钥。
// 官方 relay 的部署实现不在官方包里；本二进制与宿主客户端共用同一份自托管 wire，
// 切换 relay 只改地址（不要期望与官方 relay 的私有 wire 字节级互通）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"dshgo/internal/liveshare/relay"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run 是 testable 的入口：ctx 取消时优雅退出并返回 0。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dsh-relayd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		addr           = fs.String("addr", "127.0.0.1:7420", "监听地址（支持 127.0.0.1:0）")
		home           = fs.String("home", defaultRelayHome(), "relay 持久目录（owner token / 审计 / share 记录）")
		publicURL      = fs.String("public-url", "", "分享链接里的外部地址（默认用创建请求的 Host）")
		viewersFile    = fs.String("viewers", "", "viewer 身份目录 JSON：[{\"subjectId\":\"alice\",\"secret\":\"…\"}]")
		ownerSubject   = fs.String("owner-subject", "owner", "relay 验证出的 owner subjectId")
		ownerDisplay   = fs.String("owner-display-name", "relay owner", "relay 验证出的 owner displayName")
		allowedSubject = fs.String("allowed-subject", "", "部署级允许列表（逗号分隔）")
		printToken     = fs.Bool("print-token", false, "启动时把 owner token 打到 stdout（仅本机 bootstrap 用）")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	viewers, err := loadViewersFile(*viewersFile)
	if err != nil {
		fmt.Fprintf(stderr, "dsh-relayd: %v\n", err)
		return 1
	}
	cfg := relay.Config{
		Home:                        *home,
		PublicBaseURL:               *publicURL,
		Viewers:                     viewers,
		OwnerSubjectID:              *ownerSubject,
		OwnerDisplayName:            *ownerDisplay,
		DeploymentAllowedSubjectIDs: splitSubjects(*allowedSubject),
	}
	srv, err := relay.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "dsh-relayd: %v\n", err)
		return 1
	}
	defer srv.Close()
	for _, warning := range srv.Warnings() {
		fmt.Fprintf(stderr, "dsh-relayd: warning: %s\n", warning)
	}
	actual, err := srv.Listen(*addr)
	if err != nil {
		fmt.Fprintf(stderr, "dsh-relayd: 监听 %s 失败: %v\n", *addr, err)
		return 1
	}
	fmt.Fprintf(stdout, "dsh-relayd listening on http://%s\n", actual)
	fmt.Fprintf(stdout, "owner token file: %s\n", srv.OwnerTokenPath())
	if *printToken {
		fmt.Fprintf(stdout, "owner token: %s\n", srv.OwnerToken())
	}
	<-ctx.Done()
	return 0
}

// defaultRelayHome 决定 relay 持久目录：$DSH_RELAY_HOME > $DSH_HOME/relayd > 用户配置目录。
func defaultRelayHome() string {
	if home := strings.TrimSpace(os.Getenv("DSH_RELAY_HOME")); home != "" {
		return home
	}
	if dshHome := strings.TrimSpace(os.Getenv("DSH_HOME")); dshHome != "" {
		return filepath.Join(dshHome, "relayd")
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "dsh-relayd")
	}
	return "dsh-relayd"
}

// loadViewersFile 读 viewer 身份目录：JSON 数组，或 {"viewers":[…]}。
func loadViewersFile(path string) ([]relay.ViewerCredential, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读 viewers 文件 %s 失败: %w", path, err)
	}
	var direct []relay.ViewerCredential
	if err := json.Unmarshal(raw, &direct); err != nil {
		var wrapper struct {
			Viewers []relay.ViewerCredential `json:"viewers"`
		}
		if err2 := json.Unmarshal(raw, &wrapper); err2 != nil {
			return nil, fmt.Errorf("viewers 文件 %s 既不是数组也不是 {\"viewers\":[…]}", path)
		}
		direct = wrapper.Viewers
	}
	for i, viewer := range direct {
		if strings.TrimSpace(viewer.SubjectID) == "" || strings.TrimSpace(viewer.Secret) == "" {
			return nil, fmt.Errorf("viewers 文件第 %d 条缺 subjectId/secret", i)
		}
	}
	return direct, nil
}

func splitSubjects(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

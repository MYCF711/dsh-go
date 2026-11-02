// probe: go-plugin gRPC 端到端验证。同一份二进制既是 host 也是 plugin。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

var handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "PROBE_PLUGIN",
	MagicCookieValue: "probe-cookie-2026",
}

// 手写 ServiceDesc，避免依赖 protoc 生成代码。
type ProbeServer interface{}

var probeServiceDesc = grpc.ServiceDesc{
	ServiceName: "probe.Probe",
	HandlerType: (*ProbeServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Echo",
		Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			in := &structpb.Struct{}
			if err := dec(in); err != nil {
				return nil, err
			}
			msg := in.GetFields()["msg"].GetStringValue()
			return structpb.NewStruct(map[string]any{
				"echo":     "pong:" + msg,
				"plugin":   "internal",
				"pid":      float64(os.Getpid()),
				"gover":    "1.27.3",
				"rev":      float64(revision),
				"pluginOs": goos,
			})
		},
	}},
	Streams:  []grpc.StreamDesc{},
	Metadata: "probe.proto",
}

var pluginArg = "--probe-plugin"

const revision = 7
const goos = "windows"

type ProbePlugin struct {
	plugin.Plugin
}

func (p *ProbePlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	s.RegisterService(&probeServiceDesc, p)
	return nil
}

func (p *ProbePlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &probeClient{conn: c}, nil
}

type probeClient struct{ conn *grpc.ClientConn }

func (c *probeClient) Echo(ctx context.Context, msg string) (map[string]any, error) {
	in, err := structpb.NewStruct(map[string]any{"msg": msg})
	if err != nil {
		return nil, err
	}
	out := &structpb.Struct{}
	if err := c.conn.Invoke(ctx, "/probe.Probe/Echo", in, out, grpc.MaxCallRecvMsgSize(4<<20)); err != nil {
		return nil, err
	}
	return out.AsMap(), nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--probe-plugin":
			runPlugin()
			return
		case "--probe-plugin-loud":
			// 坑 1：握手前往 stdout 写任何东西，都会污染 magic cookie 那一行握手。
			fmt.Println("THIS LINE CORRUPTS THE HANDSHAKE")
			runPlugin()
			return
		case "--bad-proto":
			runHost(2)
			return
		case "--reattach":
			runReattach()
			return
		case "--loud-host":
			pluginArg = "--probe-plugin-loud"
			runHost(0)
			return
		}
	}
	runHost(0)
}

func runPlugin() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         map[string]plugin.Plugin{"probe": &ProbePlugin{}},
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}

func runHost(protoOverride int) {
	hold := 0
	if len(os.Args) > 2 && os.Args[1] == "--hold" {
		hold, _ = strconv.Atoi(os.Args[2])
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Println("os.Executable:", err)
		os.Exit(1)
	}

	hs := handshake
	if protoOverride != 0 {
		hs.ProtocolVersion = uint(protoOverride)
	}

	start := time.Now()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  hs,
		Plugins:          map[string]plugin.Plugin{"probe": &ProbePlugin{}},
		Cmd:              exec.Command(exe, pluginArg),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	defer client.Kill()

	rpcClient, err := client.Client()
	if err != nil {
		fmt.Println("FAIL client.Client():", err)
		os.Exit(1)
	}
	fmt.Printf("proto=%s negotiated_version=%d spawn_ms=%d\n",
		client.Protocol(), client.NegotiatedVersion(), time.Since(start).Milliseconds())

	raw, err := rpcClient.Dispense("probe")
	if err != nil {
		fmt.Println("FAIL Dispense:", err)
		os.Exit(1)
	}
	pc := raw.(*probeClient)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := pc.Echo(ctx, "hello-from-host")
	if err != nil {
		fmt.Println("FAIL Echo:", err)
		os.Exit(1)
	}
	fmt.Printf("rpc_result=%v\n", res)
	fmt.Printf("plugin_pid=%d\n", int(res["pid"].(float64)))

	if hold > 0 {
		fmt.Printf("holding %ds for memory sampling\n", hold)
		time.Sleep(time.Duration(hold) * time.Second)
	}
}

// runReattach 验证 ReattachConfig（Windows 上是否可复用同一个插件进程）。
func runReattach() {
	exe, _ := os.Executable()
	c1 := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  handshake,
		Plugins:          map[string]plugin.Plugin{"probe": &ProbePlugin{}},
		Cmd:              exec.Command(exe, pluginArg),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	r1, err := c1.Client()
	if err != nil {
		fmt.Println("FAIL c1.Client:", err)
		os.Exit(1)
	}
	raw1, _ := r1.Dispense("probe")
	ctx := context.Background()
	m1, err := raw1.(*probeClient).Echo(ctx, "first")
	if err != nil {
		fmt.Println("FAIL c1.Echo:", err)
		os.Exit(1)
	}
	fmt.Println("c1_echo=", m1["echo"])

	rc := c1.ReattachConfig()
	if rc == nil {
		fmt.Println("FAIL: ReattachConfig() == nil")
		os.Exit(1)
	}
	fmt.Printf("reattach_config: protocol=%s pid=%d addr=%s reattachFunc=%v\n",
		rc.Protocol, rc.Pid, rc.Addr, rc.ReattachFunc != nil)

	c2 := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  handshake,
		Plugins:          map[string]plugin.Plugin{"probe": &ProbePlugin{}},
		Reattach:         rc,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
	})
	r2, err := c2.Client()
	if err != nil {
		fmt.Println("FAIL c2.Client(reattach):", err)
		os.Exit(1)
	}
	raw2, _ := r2.Dispense("probe")
	m2, err := raw2.(*probeClient).Echo(ctx, "second")
	if err != nil {
		fmt.Println("FAIL c2.Echo:", err)
		os.Exit(1)
	}
	fmt.Println("c2_echo_via_reattach=", m2["echo"], "same_pid=", int(m2["pid"].(float64)) == rc.Pid)

	c1.Kill()
	time.Sleep(300 * time.Millisecond)
	if _, err := raw2.(*probeClient).Echo(ctx, "third"); err != nil {
		fmt.Println("after_kill_echo_err=", err.Error())
	}
}

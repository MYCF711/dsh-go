package main

// 官方每请求采样面在 `dshd run` 这条真实链路上的接线：
// `DSHD_LLM_TEMPERATURE` / `DSHD_LLM_STOP` 必须真的出现在发往网关的报文里，
// 不设时必须**一个键都不出现**（严格网关不接受无谓漂移）。
//
// 这里打的是真网关（httptest）+ 真 cmdRun，而不是手搓一个 llm.Request：
// 只有整条链（flag/env → agent.Loop → llm.Request → wire）都通，才算"透传了"。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestSamplingFromEnv 钉住解析层的三条：不设=零值、非法 fail-loud、逗号分隔保序。
func TestSamplingFromEnv(t *testing.T) {
	t.Run("两个都不设", func(t *testing.T) {
		sampling, err := samplingFromEnv(func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if sampling.Temperature != nil || len(sampling.Stop) != 0 {
			t.Fatalf("空环境不该产出任何采样项：%+v", sampling)
		}
	})
	t.Run("显式设置", func(t *testing.T) {
		// 环境变量是单行的：换行用 \n 转义表达（最常用的停用词就是 "\n\n"）。
		env := map[string]string{
			llmTemperatureEnv: "0.2",
			llmStopEnv:        `\n\n,STOP,END`,
		}
		sampling, err := samplingFromEnv(func(key string) string { return env[key] })
		if err != nil {
			t.Fatal(err)
		}
		if sampling.Temperature == nil || *sampling.Temperature != 0.2 {
			t.Fatalf("temperature = %v，期望 0.2", sampling.Temperature)
		}
		if len(sampling.Stop) != 3 || sampling.Stop[0] != "\n\n" || sampling.Stop[1] != "STOP" || sampling.Stop[2] != "END" {
			t.Fatalf("stop = %#v，期望按给定顺序 [\\n\\n STOP END]", sampling.Stop)
		}
	})
	t.Run("temperature=0 是显式值不是未设置", func(t *testing.T) {
		sampling, err := samplingFromEnv(func(key string) string {
			if key == llmTemperatureEnv {
				return "0"
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if sampling.Temperature == nil || *sampling.Temperature != 0 {
			t.Fatalf("temperature=0 必须解析成非 nil 的 0（确定性复现靠它），得到 %v", sampling.Temperature)
		}
	})
	t.Run("非法值 fail-loud", func(t *testing.T) {
		for _, bad := range []string{"abc", "0.2x", "NaN", "Inf", "+Inf"} {
			env := map[string]string{llmTemperatureEnv: bad}
			if _, err := samplingFromEnv(func(key string) string { return env[key] }); err == nil {
				t.Fatalf("%s=%q 被静默忽略：必须 fail-loud", llmTemperatureEnv, bad)
			}
		}
	})
	t.Run("只有逗号等于没有停用词", func(t *testing.T) {
		env := map[string]string{llmStopEnv: " , ,, "}
		sampling, err := samplingFromEnv(func(key string) string { return env[key] })
		if err != nil {
			t.Fatal(err)
		}
		if len(sampling.Stop) != 0 {
			t.Fatalf("空项必须被丢掉，得到 %#v", sampling.Stop)
		}
	})
}

// samplingCaptureServer 记录每次 chat/completions 的原始请求体，再回一格完整流。
type samplingCaptureServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newSamplingCaptureServer(t *testing.T) *samplingCaptureServer {
	t.Helper()
	fake := &samplingCaptureServer{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.bodies = append(fake.bodies, string(raw))
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (s *samplingCaptureServer) lastBody(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		t.Fatal("网关一次请求都没收到")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(s.bodies[len(s.bodies)-1]), &body); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v（%s）", err, s.bodies[len(s.bodies)-1])
	}
	return body
}

// TestRunForwardsLLMSamplingEnvToWire 是 T1 的端到端先红/后绿证据。
func TestRunForwardsLLMSamplingEnvToWire(t *testing.T) {
	cases := []struct {
		name            string
		env             map[string]string
		wantTemperature *float64
		wantStop        []string
	}{
		{name: "不设环境变量：报文里没有这两个键"},
		{
			name:            "显式设置：逐字出现",
			env:             map[string]string{llmTemperatureEnv: "0.2", llmStopEnv: `\n\n,STOP`},
			wantTemperature: ptrFloat(0.2),
			wantStop:        []string{"\n\n", "STOP"},
		},
		{
			// temperature=0 是确定性复现的唯一手段：它必须真的发出去，
			// 不能被 omitempty 当成"没设置"。
			name:            "temperature=0 也要发",
			env:             map[string]string{llmTemperatureEnv: "0"},
			wantTemperature: ptrFloat(0),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{llmTemperatureEnv, llmStopEnv} {
				if value, ok := tc.env[key]; ok {
					t.Setenv(key, value)
				} else {
					// t.Setenv 不能"取消设置"，显式写空串（解析层把它当未设置）。
					t.Setenv(key, "")
				}
			}
			fake := newSamplingCaptureServer(t)
			const sid = "sampling-wire"
			home, _ := d1Workspace(t, sid)
			if err := d1Run(t, home, sid, fake.URL); err != nil {
				t.Fatalf("run 失败: %v", err)
			}
			body := fake.lastBody(t)

			temperature, hasTemperature := body["temperature"]
			if tc.wantTemperature == nil {
				if hasTemperature {
					t.Fatalf("没设温度时报文里出现了 temperature=%v（严格网关会 400）: %v", temperature, body)
				}
			} else {
				if !hasTemperature {
					t.Fatalf("设了 %s 但报文里没有 temperature：采样面没有被透传（body=%v）",
						llmTemperatureEnv, body)
				}
				if value, ok := temperature.(float64); !ok || value != *tc.wantTemperature {
					t.Fatalf("wire temperature = %#v，期望 %v", temperature, *tc.wantTemperature)
				}
			}

			stop, hasStop := body["stop"]
			if len(tc.wantStop) == 0 {
				if hasStop {
					t.Fatalf("没设停用词时报文里出现了 stop=%v: %v", stop, body)
				}
				return
			}
			values, ok := stop.([]any)
			if !ok || len(values) != len(tc.wantStop) {
				t.Fatalf("wire stop = %#v，期望 %#v", stop, tc.wantStop)
			}
			for i, want := range tc.wantStop {
				if values[i] != want {
					t.Fatalf("wire stop[%d] = %#v，期望 %q", i, values[i], want)
				}
			}
		})
	}
}

// TestRunRejectsInvalidSamplingEnv：配置写错必须**在发请求之前**失败，
// 且错误里能看出是哪个变量写错了（否则用户只能猜）。
func TestRunRejectsInvalidSamplingEnv(t *testing.T) {
	t.Setenv(llmTemperatureEnv, "hot")
	fake := newSamplingCaptureServer(t)
	const sid = "sampling-invalid"
	home, _ := d1Workspace(t, sid)
	err := d1Run(t, home, sid, fake.URL)
	if err == nil {
		t.Fatal("非法的 DSHD_LLM_TEMPERATURE 被静默忽略")
	}
	if !strings.Contains(err.Error(), llmTemperatureEnv) {
		t.Fatalf("错误里必须点名 %s，得到: %v", llmTemperatureEnv, err)
	}
	fake.mu.Lock()
	requests := len(fake.bodies)
	fake.mu.Unlock()
	if requests != 0 {
		t.Fatalf("配置非法时仍然发了 %d 次模型请求（必须 fail-closed）", requests)
	}
}

func ptrFloat(value float64) *float64 { return &value }

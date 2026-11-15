package analytics

// OTLP/HTTP JSON（logs）编码器的逐字段验收。
//
// 断言对象是**官方序列化器的输出形状**（`@opentelemetry/otlp-transformer`
// 0.222.0 的 JsonLogsSerializer），不是"我们自己觉得像 OTLP"：
//   - 信封 `resourceLogs[].resource` / `scopeLogs[].scope`；
//   - 时间戳是十进制**字符串**纳秒（JSON_ENCODER.encodeHrTime = encodeAsString）；
//   - `severityNumber` 数值 + `severityText` 枚举名；
//   - `eventName` 是一等字段（不混进 attributes）；
//   - 属性值形状 `stringValue` / `intValue` / `doubleValue` / `boolValue` / `kvlistValue`；
//   - 没有 span 上下文时 `flags`/`traceId`/`spanId` 不出现（官方同样省略）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// otlpResourceLogsOf 把编码结果解成"一个 resource 的通用 map"。
func otlpResourceLogsOf(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("OTLP 载荷不是合法 JSON: %v（%s）", err, payload)
	}
	resourceLogs, ok := decoded["resourceLogs"].([]any)
	if !ok || len(resourceLogs) != 1 {
		t.Fatalf("resourceLogs 必须是一个元素的数组：%s", payload)
	}
	return resourceLogs[0].(map[string]any)
}

// otlpRecordsOf 取 resourceLogs[0].scopeLogs[0].logRecords 的通用 map 列表。
func otlpRecordsOf(t *testing.T, payload []byte) []map[string]any {
	t.Helper()
	resourceLog := otlpResourceLogsOf(t, payload)
	scopeLogs, ok := resourceLog["scopeLogs"].([]any)
	if !ok || len(scopeLogs) != 1 {
		t.Fatalf("scopeLogs 必须是一个元素的数组：%s", payload)
	}
	logRecords, ok := scopeLogs[0].(map[string]any)["logRecords"].([]any)
	if !ok {
		t.Fatalf("logRecords 缺失：%s", payload)
	}
	out := make([]map[string]any, 0, len(logRecords))
	for _, record := range logRecords {
		out = append(out, record.(map[string]any))
	}
	return out
}

// otlpAttributeValues 把 attributes 数组折成 key → value 形状的 map。
func otlpAttributeValues(t *testing.T, attributes any) map[string]map[string]any {
	t.Helper()
	list, ok := attributes.([]any)
	if !ok {
		t.Fatalf("attributes 不是数组：%v", attributes)
	}
	out := make(map[string]map[string]any, len(list))
	for _, item := range list {
		entry := item.(map[string]any)
		out[entry["key"].(string)] = entry["value"].(map[string]any)
	}
	return out
}

func TestOTLPJSONEncoderMatchesOfficialSDKShape(t *testing.T) {
	resource := []LogAttribute{
		{Key: "service.name", Value: "dsh-go"},
		{Key: "user.id", Value: "user-1"},
		{Key: "cpu_logical_count", Value: int64(16)},
		{Key: "has_gpu", Value: true},
		{Key: "sampling_ratio", Value: 0.5},
		{Key: "nested", Value: map[string]any{"mode": "batch"}},
	}
	scope := InstrumentationScope{Name: "dshgo/internal/analytics", Version: "9.8.7"}
	payload, err := marshalOTLPLogs(resource, scope, []otlpLogRecord{{
		EventName:        "send_button_click",
		Body:             "send_button_click",
		TimeUnixNano:     1730000000000000000,
		ObservedTimeNano: 1730000000123456789,
		SeverityNumber:   SeverityNumberWarn,
		Attributes: []LogAttribute{
			{Key: "model_name", Value: "deepseek-chat"},
			{Key: "is_success", Value: true},
			{Key: "duration", Value: 12.25},
			{Key: "policyVersion", Value: int64(4)},
		},
	}})
	if err != nil {
		t.Fatalf("marshalOTLPLogs: %v", err)
	}

	resourceLog := otlpResourceLogsOf(t, payload)
	resourceAttributes := otlpAttributeValues(t, resourceLog["resource"].(map[string]any)["attributes"])
	if resourceLog["resource"].(map[string]any)["droppedAttributesCount"] != float64(0) {
		t.Fatalf("resource.droppedAttributesCount 必须是 0：%v", resourceLog["resource"])
	}
	wantResource := map[string]string{
		"service.name":      `{"stringValue":"dsh-go"}`,
		"user.id":           `{"stringValue":"user-1"}`,
		"cpu_logical_count": `{"intValue":16}`,
		"has_gpu":           `{"boolValue":true}`,
		"sampling_ratio":    `{"doubleValue":0.5}`,
		"nested":            `{"kvlistValue":{"values":[{"key":"mode","value":{"stringValue":"batch"}}]}}`,
	}
	for key, want := range wantResource {
		raw, err := json.Marshal(resourceAttributes[key])
		if err != nil {
			t.Fatalf("序列化 resource 属性 %s: %v", key, err)
		}
		if string(raw) != want {
			t.Fatalf("resource 属性 %s = %s，期望 %s", key, raw, want)
		}
	}

	scopeLogs := resourceLog["scopeLogs"].([]any)[0].(map[string]any)
	scopeJSON, err := json.Marshal(scopeLogs["scope"])
	if err != nil {
		t.Fatalf("序列化 scope: %v", err)
	}
	if string(scopeJSON) != `{"name":"dshgo/internal/analytics","version":"9.8.7"}` {
		t.Fatalf("scope = %s", scopeJSON)
	}

	records := otlpRecordsOf(t, payload)
	if len(records) != 1 {
		t.Fatalf("logRecords = %d", len(records))
	}
	record := records[0]

	// 时间戳必须是**字符串**纳秒（官方 encodeAsString），不是数字。
	rawRecord := scopeLogs["logRecords"].([]any)[0].(map[string]any)
	if got, ok := rawRecord["timeUnixNano"].(string); !ok || got != "1730000000000000000" {
		t.Fatalf("timeUnixNano 必须是字符串纳秒：%v (%T)", rawRecord["timeUnixNano"], rawRecord["timeUnixNano"])
	}
	if got, ok := rawRecord["observedTimeUnixNano"].(string); !ok || got != "1730000000123456789" {
		t.Fatalf("observedTimeUnixNano 必须是字符串纳秒：%v", rawRecord["observedTimeUnixNano"])
	}
	if record["severityNumber"] != float64(SeverityNumberWarn) || record["severityText"] != "WARN" {
		t.Fatalf("severity = %v / %v", record["severityNumber"], record["severityText"])
	}
	bodyJSON, _ := json.Marshal(record["body"])
	if string(bodyJSON) != `{"stringValue":"send_button_click"}` {
		t.Fatalf("body = %s", bodyJSON)
	}
	if record["eventName"] != "send_button_click" {
		t.Fatalf("eventName = %v（官方是 logRecord 的一等字段）", record["eventName"])
	}
	attributes := otlpAttributeValues(t, record["attributes"])
	wantAttributes := map[string]string{
		"model_name":    `{"stringValue":"deepseek-chat"}`,
		"is_success":    `{"boolValue":true}`,
		"duration":      `{"doubleValue":12.25}`,
		"policyVersion": `{"intValue":4}`,
	}
	for key, want := range wantAttributes {
		raw, err := json.Marshal(attributes[key])
		if err != nil {
			t.Fatalf("序列化属性 %s: %v", key, err)
		}
		if string(raw) != want {
			t.Fatalf("属性 %s = %s，期望 %s", key, raw, want)
		}
	}

	// 没有 span 上下文：官方 JSON.stringify 会把 undefined 省掉，宿主也一样。
	for _, absent := range []string{`"traceId"`, `"spanId"`, `"flags"`} {
		if strings.Contains(string(payload), absent) {
			t.Fatalf("无 span 上下文时不该出现 %s：%s", absent, payload)
		}
	}
}

func TestSeverityTextFollowsAPILogsNames(t *testing.T) {
	cases := map[int]string{
		SeverityNumberUnspecified: "",
		SeverityNumberDebug:       "DEBUG",
		SeverityNumberInfo:        "INFO",
		10:                        "INFO2",
		SeverityNumberWarn:        "WARN",
		SeverityNumberError:       "ERROR",
		SeverityNumberFatal:       "FATAL",
		99:                        "",
	}
	for number, want := range cases {
		if got := severityTextFor(number); got != want {
			t.Fatalf("severityTextFor(%d) = %q，期望 %q", number, got, want)
		}
	}
}

// 毫秒 → 纳秒必须是精确整数：float 乘法会在 1.7e18 量级丢精度。
func TestLogTimestampUnixNanoIsExact(t *testing.T) {
	cases := map[float64]int64{
		1730000000000:    1730000000000000000,
		1730000000123:    1730000000123000000,
		1730000000000.75: 1730000000001000000, // 四舍五入到整毫秒
		0:                0,
	}
	for millis, want := range cases {
		if got := logTimestampUnixNano(millis); got != want {
			t.Fatalf("logTimestampUnixNano(%v) = %d，期望 %d", millis, got, want)
		}
	}
}

func TestOTLPEncoderRejectsUnsupportedValues(t *testing.T) {
	_, err := marshalOTLPLogs(nil, InstrumentationScope{Name: "scope"}, []otlpLogRecord{{
		EventName: "e", Body: "e", Attributes: []LogAttribute{{Key: "list", Value: []any{"a"}}},
	}})
	if err == nil || !strings.Contains(err.Error(), "数组") {
		t.Fatalf("数组属性必须报错（不假装支持）：%v", err)
	}
	if _, err := marshalOTLPLogs(nil, InstrumentationScope{}, nil); err == nil {
		t.Fatal("空 scope name 必须报错")
	}
	if _, err := marshalOTLPLogs(nil, InstrumentationScope{Name: "s"}, []otlpLogRecord{{
		EventName: "e", Attributes: []LogAttribute{{Key: "", Value: "x"}},
	}}); err == nil {
		t.Fatal("空属性键必须报错")
	}
}

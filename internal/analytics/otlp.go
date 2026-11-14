package analytics

// OTLP/HTTP JSON（logs）编码器 —— 手写 struct，不引入任何依赖。
//
// 为什么必须手写：本仓零依赖纪律（`go.mod` 只有一个 module 行），而官方这条链
// 的载荷是 @opentelemetry/otlp-transformer 的 `JsonLogsSerializer` 产物。要"真
// OTLP"就只剩两条路：引依赖（不允许），或照官方序列化器的字段名/值形状手写。
//
// 官方原件（只读；dsh-otel 把这三个包锁在 0.222.0 / 2.11.0）：
//
//   - `@opentelemetry/otlp-transformer@0.222.0/build/esm/logs/internal.js`
//     `createExportLogsServiceRequest` → `{ resourceLogs: [{ resource, scopeLogs:
//     [{ scope, logRecords }] }] }`；
//     `toLogRecord` 逐字段是 `timeUnixNano`、`observedTimeUnixNano`、
//     `severityNumber`、`severityText`、`body`、**`eventName`**、`attributes`、
//     `droppedAttributesCount`（`flags`/`traceId`/`spanId` 缺省时由
//     `JSON.stringify` 省掉）。
//   - `build/esm/common/utils.js`：`JSON_ENCODER.encodeHrTime = encodeAsString`
//     → 两个时间戳都是**十进制字符串**纳秒（不是数字：超过了 JSON number 的精确范围）。
//   - `build/esm/common/internal.js`：`createResource` → `{ attributes,
//     droppedAttributesCount: 0 }`；`createInstrumentationScope` → `{ name, version }`；
//     `toAnyValue` → string=`stringValue`、整数 number=`intValue`、
//     非整数 number=`doubleValue`、boolean=`boolValue`、对象=`kvlistValue`。
//   - `@opentelemetry/sdk-logs@0.222.0/build/esm/LogRecordImpl.js`：`eventName` 是
//     LogRecord 的**一等字段**（`_eventName`），不混进 attributes；这也解释了
//     官方 session-log 通道为什么写 `eventName: "session-log"` 而不是塞属性。
//
// 形状因此与官方逐字段一致；唯一没抄的是 `flags`/`traceId`/`spanId`
// （本仓记录里没有 span 上下文，官方在缺省时同样省略）。

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// SeverityNumber 是官方 @opentelemetry/api-logs 的数值（INFO = 9）。
//
// 只定义本仓真实会用到的档位：普通事件与会话日志都默认 INFO；其余档位留给
// 消费者显式选择（数值逐字来自 api-logs 枚举，不能自造）。
const (
	SeverityNumberUnspecified = 0
	SeverityNumberDebug       = 5
	SeverityNumberInfo        = 9
	SeverityNumberWarn        = 13
	SeverityNumberError       = 17
	SeverityNumberFatal       = 21
)

// severityTextFor 是官方 `SeverityNumber[severityNumber]` 的宿主实现
// （TS 数值枚举的反查名字）：9 → "INFO"、13 → "WARN"、17 → "ERROR"。
//
// 未定义的数值返回空串（`severityText` 用 omitempty 省掉），不编一个假名字。
func severityTextFor(number int) string {
	switch number {
	case 1:
		return "TRACE"
	case 2:
		return "TRACE2"
	case 3:
		return "TRACE3"
	case 4:
		return "TRACE4"
	case 5:
		return "DEBUG"
	case 6:
		return "DEBUG2"
	case 7:
		return "DEBUG3"
	case 8:
		return "DEBUG4"
	case 9:
		return "INFO"
	case 10:
		return "INFO2"
	case 11:
		return "INFO3"
	case 12:
		return "INFO4"
	case 13:
		return "WARN"
	case 14:
		return "WARN2"
	case 15:
		return "WARN3"
	case 16:
		return "WARN4"
	case 17:
		return "ERROR"
	case 18:
		return "ERROR2"
	case 19:
		return "ERROR3"
	case 20:
		return "ERROR4"
	case 21:
		return "FATAL"
	case 22:
		return "FATAL2"
	case 23:
		return "FATAL3"
	case 24:
		return "FATAL4"
	default:
		return ""
	}
}

// LogAttribute 是一条日志/资源属性（OTLP KeyValue 的宿主形状）。
//
// 顺序即编码顺序（官方 `toAttributes` 按 `Object.keys` 顺序）：调用方给的顺序
// 就是最终字节顺序，**不重排**；需要确定性顺序的调用方自己排序（见
// sortedLogAttributes）。
//
// Value 只允许标量（string/bool/整数/浮点）或一层对象（对象值仍递归编码成
// kvlistValue）。其它类型在入队前就报错——不静默转成字符串，那是假数据。
type LogAttribute struct {
	Key   string
	Value any
}

// otlpAnyValue 是 OTLP `AnyValue` 的 JSON 形状（官方 toAnyValue 同款）。
//
// 指针 + omitempty 是必须的：`"intValue": 0` / `"boolValue": false` / 空串都是
// 合法值，零值不能跟"没有这个成员"混在一起。
type otlpAnyValue struct {
	StringValue *string          `json:"stringValue,omitempty"`
	IntValue    *int64           `json:"intValue,omitempty"`
	DoubleValue *float64         `json:"doubleValue,omitempty"`
	BoolValue   *bool            `json:"boolValue,omitempty"`
	KvlistValue *otlpKvlistValue `json:"kvlistValue,omitempty"`
}

type otlpKvlistValue struct {
	Values []otlpKeyValue `json:"values"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpResource struct {
	Attributes             []otlpKeyValue `json:"attributes"`
	DroppedAttributesCount int            `json:"droppedAttributesCount"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpScopeLogs struct {
	Scope      otlpScope           `json:"scope"`
	LogRecords []otlpWireLogRecord `json:"logRecords"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

// otlpLogsRequest 是一次 OTLP/HTTP logs 导出的完整请求体。
type otlpLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

// otlpLogRecord 是一条已规范化、可直接编码的日志记录。
//
// 时间是**纳秒整数**，编码时转十进制字符串（见文件头的 JSON_ENCODER 说明）。
type otlpLogRecord struct {
	EventName        string
	Body             string
	TimeUnixNano     int64
	ObservedTimeNano int64
	SeverityNumber   int
	SeverityText     string
	Attributes       []LogAttribute
}

type otlpWireLogRecord struct {
	TimeUnixNano           string         `json:"timeUnixNano"`
	ObservedTimeUnixNano   string         `json:"observedTimeUnixNano"`
	SeverityNumber         int            `json:"severityNumber"`
	SeverityText           string         `json:"severityText,omitempty"`
	Body                   otlpAnyValue   `json:"body"`
	EventName              string         `json:"eventName,omitempty"`
	Attributes             []otlpKeyValue `json:"attributes"`
	DroppedAttributesCount int            `json:"droppedAttributesCount"`
}

// InstrumentationScope 是 OTLP instrumentation scope（官方由每个消费者自己给：
// "Scope names and versions are supplied by each consumer"，dsh-otel README:38）。
type InstrumentationScope struct {
	Name    string
	Version string
}

// logTimestampUnixNano 把"Unix 毫秒"换成纳秒整数。
//
// 不走 float 乘法（1.7e12 × 1e6 会撞 float64 的 53 位精度，纳秒级抖几百）：
// 先拆整毫秒，再分别乘到秒/毫秒位上，结果精确。
func logTimestampUnixNano(millis float64) int64 {
	rounded := int64(math.Round(millis))
	seconds := rounded / 1000
	remainder := rounded % 1000
	return seconds*1e9 + remainder*1e6
}

// otlpValueOf 把宿主值翻成 OTLP AnyValue（官方 toAnyValue 的分支逐个对上）。
func otlpValueOf(value any) (otlpAnyValue, error) {
	switch typed := value.(type) {
	case nil:
		return otlpAnyValue{}, fmt.Errorf("OTLP 属性值是 nil（官方 toAnyValue 会退化成空对象，宿主这里直接拒绝）")
	case string:
		return otlpAnyValue{StringValue: &typed}, nil
	case bool:
		return otlpAnyValue{BoolValue: &typed}, nil
	case int:
		integer := int64(typed)
		return otlpAnyValue{IntValue: &integer}, nil
	case int32:
		integer := int64(typed)
		return otlpAnyValue{IntValue: &integer}, nil
	case int64:
		integer := typed
		return otlpAnyValue{IntValue: &integer}, nil
	case float32:
		return otlpValueOf(float64(typed))
	case float64:
		// 官方：整数 number → intValue，非整数 → doubleValue。照着分。
		if typed == math.Trunc(typed) && !math.IsInf(typed, 0) && math.Abs(typed) < 1<<53 {
			integer := int64(typed)
			return otlpAnyValue{IntValue: &integer}, nil
		}
		number := typed
		return otlpAnyValue{DoubleValue: &number}, nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return otlpAnyValue{IntValue: &integer}, nil
		}
		number, err := typed.Float64()
		if err != nil {
			return otlpAnyValue{}, fmt.Errorf("OTLP 属性值 %q 既不是整数也不是浮点: %w", typed.String(), err)
		}
		return otlpAnyValue{DoubleValue: &number}, nil
	case map[string]any:
		// 官方允许一层（乃至多层）对象：递归编码成 kvlistValue。
		values := make([]otlpKeyValue, 0, len(typed))
		for key, child := range typed {
			converted, err := otlpValueOf(child)
			if err != nil {
				return otlpAnyValue{}, fmt.Errorf("OTLP 属性 %q: %w", key, err)
			}
			values = append(values, otlpKeyValue{Key: key, Value: converted})
		}
		return otlpAnyValue{KvlistValue: &otlpKvlistValue{Values: values}}, nil
	case []any:
		// 数组（arrayValue）本仓产品分析的白名单里不存在；不假装支持。
		return otlpAnyValue{}, fmt.Errorf("OTLP 属性值不支持数组（本仓属性是标量或一层对象）")
	default:
		return otlpAnyValue{}, fmt.Errorf("OTLP 属性值类型 %T 不支持", value)
	}
}

// otlpAttributes 把有序属性编成 OTLP KeyValue 列表（空值/空键提前报错）。
func otlpAttributes(attributes []LogAttribute) ([]otlpKeyValue, error) {
	out := make([]otlpKeyValue, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Key == "" {
			return nil, fmt.Errorf("OTLP 属性键不能为空")
		}
		converted, err := otlpValueOf(attribute.Value)
		if err != nil {
			return nil, fmt.Errorf("OTLP 属性 %q: %w", attribute.Key, err)
		}
		out = append(out, otlpKeyValue{Key: attribute.Key, Value: converted})
	}
	return out, nil
}

// marshalOTLPLogs 编出一条 OTLP/HTTP JSON 请求体。
//
// 结构对应官方 `createExportLogsServiceRequest`：一个 resource、一个 scope、
// 一条（或一批）logRecord。空记录列表按 OTLP 语义是合法请求（`logRecords: []`），
// 但调用方不该发它——通道在 `post` 之前就会把空批次挡掉。
func marshalOTLPLogs(resource []LogAttribute, scope InstrumentationScope, records []otlpLogRecord) ([]byte, error) {
	if scope.Name == "" {
		return nil, fmt.Errorf("OTLP instrumentation scope 的 name 不能为空")
	}
	resourceAttributes, err := otlpAttributes(resource)
	if err != nil {
		return nil, err
	}
	wireRecords := make([]otlpWireLogRecord, 0, len(records))
	for _, record := range records {
		attributes, err := otlpAttributes(record.Attributes)
		if err != nil {
			return nil, err
		}
		body := record.Body
		severityText := record.SeverityText
		if severityText == "" {
			severityText = severityTextFor(record.SeverityNumber)
		}
		wireRecords = append(wireRecords, otlpWireLogRecord{
			TimeUnixNano:         strconv.FormatInt(record.TimeUnixNano, 10),
			ObservedTimeUnixNano: strconv.FormatInt(record.ObservedTimeNano, 10),
			SeverityNumber:       record.SeverityNumber,
			SeverityText:         severityText,
			Body:                 otlpAnyValue{StringValue: &body},
			EventName:            record.EventName,
			Attributes:           attributes,
		})
	}
	payload := otlpLogsRequest{ResourceLogs: []otlpResourceLogs{{
		Resource: otlpResource{Attributes: resourceAttributes, DroppedAttributesCount: 0},
		ScopeLogs: []otlpScopeLogs{{
			// 两个结构体字段完全一致（Name/Version），直接用转换。
			// 原先写成 struct literal 被 staticcheck S1016 抓出来 —— 是**真提示**：
			// 手抄字段名会在将来某一侧加字段时静默丢掉。
			Scope:      otlpScope(scope),
			LogRecords: wireRecords,
		}},
	}}}
	return json.Marshal(payload)
}

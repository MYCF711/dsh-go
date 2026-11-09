package transport

// 方法描述符层（官方插件面 #9 `typert` / #10 `typert-loader` / #11 `typert-gateway`）。
//
// 基线的问题不是"没有方法表"——SessionMethods() 就是一张表；问题是那张表是
// **编译期常量**：进程跑起来之后没人能加一条描述，也没有任何地方按描述校验参数。
// 于是每个 handler 各自 `json.Decode` 一次，40 个 handler 就有 40 种错法
// （有的 400、有的静默忽略未知字段、有的 500）。
//
// 本文件提供三件事，且**不改变任何既有 handler 的行为**（接线由 Lead 决定，见
// WIRING.md）：
//
//  1. 运行时注册：`Registry.Register` 立刻生效，重名/坏描述符一律报错；
//  2. 按描述校验参数：`ValidateParams` / `Decode`，错参数回 `*ParamError`
//     （带 code / param / hint，能直接落成四字段错误信封）；
//  3. 稳定快照：`DescriptorSnapshot()` 的字节可进 golden，给 Lead 审查官方方法面。
//
// 边界（明确不做）：
//   - 不替 handler 决定 HTTP 状态码——那是 handler 的事，这里只给"哪个参数错、错在哪"；
//   - 不做 RemoteStream/双向流（#11 的另一半），那属于 transport 会话层；
//   - canonical 25 的描述里的 `required` 一律 false：这是**描述**不是**门禁**。
//     逐方法的必填判定必须对照 handler 才能打开，否则会把合法客户端拦在门外。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// ParamKind 是参数类型。字符串就是线协议上的值。
type ParamKind string

const (
	ParamString  ParamKind = "string"
	ParamBool    ParamKind = "boolean"
	ParamNumber  ParamKind = "number"
	ParamInteger ParamKind = "integer"
	ParamObject  ParamKind = "object"
	ParamArray   ParamKind = "array"
	ParamAny     ParamKind = "any"
)

// PackageSchema 是挂载包描述文件的 schema（typert-loader 形状）。
const PackageSchema = "dsh.typert-package@1"

// DescriptorSnapshotSchema 是描述符快照文件的 schema。
const DescriptorSnapshotSchema = "dsh.typert-descriptors@1"

// maxPackageBytes 是包描述文件的上限：描述是元数据，1 MiB 已经非常宽。
const maxPackageBytes = 1 << 20

// ParamSpec 描述一个参数。
type ParamSpec struct {
	Name     string    `json:"name"`
	Kind     ParamKind `json:"kind"`
	Required bool      `json:"required"`
	// Enum 只对 string 有意义（枚举 ID 都是字符串）。
	Enum []string `json:"enum,omitempty"`
	// Items 只对 array 有意义：数组元素的类型（只校验一层，嵌套数组用 any）。
	Items ParamKind `json:"items,omitempty"`
	// MinLength / MaxLength 对 string 按 rune 计、对 array 按元素个数计。
	MinLength int `json:"minLength,omitempty"`
	MaxLength int `json:"maxLength,omitempty"`
}

// Descriptor 是一个方法的描述。
type Descriptor struct {
	Method  Method      `json:"method"`
	Summary string      `json:"summary"`
	Params  []ParamSpec `json:"params"`
	Result  string      `json:"result,omitempty"`
	// Source 说明描述从哪来：canonical / additive:* / package:<name> / runtime。
	Source string `json:"source"`
	// Revision 是注册代数（每次成功注册 +1），客户端靠它判断"描述变了没"。
	// 快照里省略（0）：golden 不该因为别的包先注册了一条就整体漂移。
	Revision int64 `json:"revision,omitempty"`
}

// ParamError 是"这次调用的参数不合描述符"。
//
// 它故意不复用 transport 的四字段信封：这一层不知道 HTTP 状态码，只给
// code（机器可判）、param（哪个参数）、hint（下一步做什么）。handler 里
// 把它翻成 400 + `errorBodyFor("bad_request", …)` 即可。
type ParamError struct {
	Method string
	Param  string
	Code   string
	Detail string
	Hint   string
}

// ParamError 的 code 取值。它们是稳定字符串——客户端按 code 分支，不按中文猜。
const (
	ParamCodeMissing      = "missing"
	ParamCodeType         = "type"
	ParamCodeEnum         = "enum"
	ParamCodeUnknown      = "unknown"
	ParamCodeLength       = "length"
	ParamCodeItems        = "items"
	ParamCodeJSON         = "json"
	ParamCodeUnregistered = "unregistered"
)

func (e *ParamError) Error() string {
	if e.Param == "" {
		return fmt.Sprintf("%s 的参数不合法（%s）：%s", e.Method, e.Code, e.Detail)
	}
	return fmt.Sprintf("%s 的参数 %q 不合法（%s）：%s", e.Method, e.Param, e.Code, e.Detail)
}

// Registry 是"当前进程里知道的方法"。
//
// 它只加不改：同一个方法名第二次注册就是错误（静默覆盖会让两个模块抢同一个
// 方法名而不自知）。并发安全。
type Registry struct {
	mu       sync.RWMutex
	byMethod map[Method]Descriptor
	seq      int64
}

// NewRegistry 造一个空注册表。
func NewRegistry() *Registry {
	return &Registry{byMethod: map[Method]Descriptor{}}
}

// Register 注册一个描述符。成功之后立刻对 ValidateParams / Decode 生效。
func (r *Registry) Register(d Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registerLocked(d)
}

func (r *Registry) registerLocked(d Descriptor) error {
	candidate := cloneDescriptor(d)
	if err := validateDescriptor(&candidate); err != nil {
		return err
	}
	if _, exists := r.byMethod[candidate.Method]; exists {
		return fmt.Errorf("方法 %q 已经有描述符了：描述符只加不改（要换实现请换方法名，不要抢别人的名字）", candidate.Method)
	}
	if candidate.Source == "" {
		candidate.Source = "runtime"
	}
	r.seq++
	candidate.Revision = r.seq
	r.byMethod[candidate.Method] = candidate
	return nil
}

// RegisterAll 原子地注册一批：任何一条不合法（或重名），一条都不进。
func (r *Registry) RegisterAll(list []Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	staged := make(map[Method]Descriptor, len(list))
	order := make([]Method, 0, len(list))
	for index := range list {
		candidate := cloneDescriptor(list[index])
		if err := validateDescriptor(&candidate); err != nil {
			return fmt.Errorf("第 %d 条描述符不合法：%w", index+1, err)
		}
		if _, exists := r.byMethod[candidate.Method]; exists {
			return fmt.Errorf("第 %d 条：方法 %q 已经有描述符了", index+1, candidate.Method)
		}
		if _, duplicated := staged[candidate.Method]; duplicated {
			return fmt.Errorf("第 %d 条：同一批里 %q 重复", index+1, candidate.Method)
		}
		if candidate.Source == "" {
			candidate.Source = "runtime"
		}
		staged[candidate.Method] = candidate
		order = append(order, candidate.Method)
	}
	// 按入参顺序分配代数：同一批的 revision 必须可复现（快照要稳定）。
	for _, method := range order {
		candidate := staged[method]
		r.seq++
		candidate.Revision = r.seq
		r.byMethod[method] = candidate
	}
	return nil
}

type packageDoc struct {
	Schema  string       `json:"schema"`
	Name    string       `json:"name"`
	Version string       `json:"version,omitempty"`
	Methods []Descriptor `json:"methods"`
}

// RegisterPackage 是 typert-loader：把一个挂载包声明的描述注入注册表。
//
// 全有或全无：坏一条就一条都不进（半批可见比整批失败更难查）。
// 包名必须与入参一致——否则来源标注（Source）就会撒谎。
func (r *Registry) RegisterPackage(name string, reader io.Reader) (int, error) {
	if strings.TrimSpace(name) == "" {
		return 0, fmt.Errorf("包名不能为空")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxPackageBytes+1))
	if err != nil {
		return 0, fmt.Errorf("读包描述 %s 失败：%w", name, err)
	}
	if len(raw) > maxPackageBytes {
		return 0, fmt.Errorf("包描述 %s 超过 %d 字节上限", name, maxPackageBytes)
	}
	var doc packageDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, fmt.Errorf("包描述 %s 不是合法 JSON：%w", name, err)
	}
	if doc.Schema != PackageSchema {
		return 0, fmt.Errorf("包描述 %s 的 schema 是 %q，只认 %q", name, doc.Schema, PackageSchema)
	}
	if doc.Name != name {
		return 0, fmt.Errorf("包描述里的 name 是 %q，与加载时给的 %q 不一致（来源标注会撒谎）", doc.Name, name)
	}
	if len(doc.Methods) == 0 {
		return 0, fmt.Errorf("包 %s 没有声明任何方法：空包是调用方的 bug，不是成功", name)
	}
	source := "package:" + name
	methods := make([]Descriptor, 0, len(doc.Methods))
	for index := range doc.Methods {
		method := doc.Methods[index]
		if method.Source == "" {
			method.Source = source
		}
		methods = append(methods, method)
	}
	if err := r.RegisterAll(methods); err != nil {
		return 0, fmt.Errorf("包 %s 的描述没有注入（整批回滚）：%w", name, err)
	}
	return len(methods), nil
}

// Lookup 查一个方法。
func (r *Registry) Lookup(method string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.byMethod[Method(method)]
	if !ok {
		return Descriptor{}, false
	}
	return cloneDescriptor(descriptor), true
}

// Len 是已注册的方法数。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byMethod)
}

// List 按方法名升序返回全部描述符（稳定顺序，快照依赖它）。
func (r *Registry) List() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.byMethod))
	for _, descriptor := range r.byMethod {
		out = append(out, cloneDescriptor(descriptor))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

type descriptorDoc struct {
	Schema  string       `json:"schema"`
	Methods []Descriptor `json:"methods"`
}

// JSON 导出稳定快照：schema + 按方法名排序的全部描述符。
func (r *Registry) JSON() ([]byte, error) {
	return marshalDescriptorDoc(r.List())
}

func marshalDescriptorDoc(methods []Descriptor) ([]byte, error) {
	normalized := make([]Descriptor, 0, len(methods))
	for _, method := range methods {
		normalized = append(normalized, cloneDescriptor(method))
	}
	doc := descriptorDoc{Schema: DescriptorSnapshotSchema, Methods: normalized}
	if doc.Methods == nil {
		doc.Methods = []Descriptor{}
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("描述符快照序列化失败：%w", err)
	}
	return buf.Bytes(), nil
}

// Decode 先把请求体解成对象，再按描述符校验。
//
// 空 body 视作 `{}`：这样"没有任何参数"的方法不用客户端硬塞一个花括号。
func (r *Registry) Decode(method string, raw []byte) (map[string]any, error) {
	body := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, &ParamError{
				Method: method, Code: ParamCodeJSON, Detail: err.Error(),
				Hint: "请求体必须是 JSON 对象（不是数组、不是裸字符串）",
			}
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	if err := r.ValidateParams(method, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ValidateParams 按描述符校验一个**已解码**的参数对象。
//
// 严格模式：未知参数是错误而不是忽略——写错参数名被静默忽略，用户在界面上
// 看到的是"改了没生效"，而日志里什么都没有。
func (r *Registry) ValidateParams(method string, body map[string]any) error {
	descriptor, ok := r.Lookup(method)
	if !ok {
		return &ParamError{
			Method: method, Code: ParamCodeUnregistered,
			Detail: "这个方法没有描述符，无法校验参数",
			Hint:   "先注册描述符（Registry.Register），或核对方法名拼写",
		}
	}

	specs := make(map[string]ParamSpec, len(descriptor.Params))
	for _, spec := range descriptor.Params {
		specs[spec.Name] = spec
	}
	for name := range body {
		if _, known := specs[name]; !known {
			return &ParamError{
				Method: method, Param: name, Code: ParamCodeUnknown,
				Detail: fmt.Sprintf("描述符里没有这个参数（合法参数：%s）", strings.Join(paramNames(descriptor.Params), ", ")),
				Hint:   "删掉不认识的参数；宿主不会静默忽略打错的名字",
			}
		}
	}
	for _, spec := range descriptor.Params {
		value, present := body[spec.Name]
		if !present {
			if spec.Required {
				return &ParamError{
					Method: method, Param: spec.Name, Code: ParamCodeMissing,
					Detail: fmt.Sprintf("必填参数缺失（类型 %s）", spec.Kind),
					Hint:   "补上这个参数后重试",
				}
			}
			continue
		}
		if err := validateValue(method, spec, value); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(method string, spec ParamSpec, value any) error {
	actual, ok := kindOfValue(value)
	if !ok || !kindMatches(spec.Kind, actual, value) {
		return &ParamError{
			Method: method, Param: spec.Name, Code: ParamCodeType,
			Detail: fmt.Sprintf("要 %s，拿到 %s（%v）", spec.Kind, describeValue(value), value),
			Hint:   "按描述符的类型传：数字不要加引号，布尔用 true/false，对象用 {}",
		}
	}

	if len(spec.Enum) > 0 {
		text, isString := value.(string)
		if !isString || !containsString(spec.Enum, text) {
			return &ParamError{
				Method: method, Param: spec.Name, Code: ParamCodeEnum,
				Detail: fmt.Sprintf("只能取 %s，拿到 %v", strings.Join(spec.Enum, " | "), value),
				Hint:   "取枚举里的值；不在枚举里的值宿主不会猜你的意思",
			}
		}
	}

	switch typed := value.(type) {
	case string:
		if length := utf8.RuneCountInString(typed); violatesLength(length, spec.MinLength, spec.MaxLength) {
			return &ParamError{
				Method: method, Param: spec.Name, Code: ParamCodeLength,
				Detail: fmt.Sprintf("长度 %d 不在 [%d, %d] 内", length, spec.MinLength, maxOrInfinite(spec.MaxLength)),
				Hint:   "调整这个参数的长度后重试",
			}
		}
	case []any:
		if violatesLength(len(typed), spec.MinLength, spec.MaxLength) {
			return &ParamError{
				Method: method, Param: spec.Name, Code: ParamCodeLength,
				Detail: fmt.Sprintf("元素个数 %d 不在 [%d, %d] 内", len(typed), spec.MinLength, maxOrInfinite(spec.MaxLength)),
				Hint:   "调整数组长度后重试",
			}
		}
		if spec.Items != "" && spec.Items != ParamAny {
			for index, element := range typed {
				elementKind, ok := kindOfValue(element)
				if !ok || !kindMatches(spec.Items, elementKind, element) {
					return &ParamError{
						Method: method, Param: spec.Name, Code: ParamCodeItems,
						Detail: fmt.Sprintf("第 %d 个元素要 %s，拿到 %s（%v）", index, spec.Items, describeValue(element), element),
						Hint:   "数组里每个元素的类型要一致",
					}
				}
			}
		}
	}
	return nil
}

// kindOfValue 给出一个解码后 JSON 值的类型。
func kindOfValue(value any) (ParamKind, bool) {
	switch value.(type) {
	case nil:
		return "", false
	case string:
		return ParamString, true
	case bool:
		return ParamBool, true
	case float64:
		return ParamNumber, true
	case float32:
		return ParamNumber, true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return ParamNumber, true
	case json.Number:
		return ParamNumber, true
	case map[string]any:
		return ParamObject, true
	case []any:
		return ParamArray, true
	default:
		return "", false
	}
}

// kindMatches 是"期望类型 vs 实际类型"。integer 是 number 的子集：
// JSON 里没有整数类型，2.0 传过来也是 float64，所以判的是"值是不是整数"。
func kindMatches(want, got ParamKind, value any) bool {
	if want == ParamAny {
		return true
	}
	if want == got {
		if want == ParamInteger {
			number, ok := numericValue(value)
			return ok && number == math.Trunc(number) && !math.IsInf(number, 0)
		}
		return true
	}
	if want == ParamInteger && got == ParamNumber {
		number, ok := numericValue(value)
		return ok && number == math.Trunc(number) && !math.IsInf(number, 0)
	}
	return false
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func describeValue(value any) ParamKind {
	kind, ok := kindOfValue(value)
	if !ok {
		return ParamKind(fmt.Sprintf("%T", value))
	}
	return kind
}

func violatesLength(length, minLength, maxLength int) bool {
	if minLength > 0 && length < minLength {
		return true
	}
	if maxLength > 0 && length > maxLength {
		return true
	}
	return false
}

func maxOrInfinite(maxLength int) int {
	if maxLength <= 0 {
		return math.MaxInt32
	}
	return maxLength
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func paramNames(specs []ParamSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Name)
	}
	return out
}

func cloneDescriptor(d Descriptor) Descriptor {
	clone := d
	if d.Params == nil {
		// 线协议上"没有参数"是 []，不是 null：客户端到处判 null 迟早漏一处。
		clone.Params = []ParamSpec{}
	} else {
		clone.Params = make([]ParamSpec, len(d.Params))
		for index, spec := range d.Params {
			clone.Params[index] = spec
			if spec.Enum != nil {
				clone.Params[index].Enum = append([]string(nil), spec.Enum...)
			}
		}
	}
	return clone
}

// validateDescriptor 校验描述符**自身**：坏描述符比没有描述符更糟——
// 它会让合法请求被拒，而错误信息看起来像客户端的错。
func validateDescriptor(d *Descriptor) error {
	name := string(d.Method)
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("方法名不能为空")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("方法名 %q 首尾不能有空白", name)
	}

	seen := make(map[string]bool, len(d.Params))
	sawOptional := false
	for index := range d.Params {
		spec := &d.Params[index]
		if strings.TrimSpace(spec.Name) == "" {
			return fmt.Errorf("方法 %q 的第 %d 个参数没有名字", name, index+1)
		}
		if seen[spec.Name] {
			return fmt.Errorf("方法 %q 的参数 %q 重复", name, spec.Name)
		}
		seen[spec.Name] = true

		if !validParamKind(spec.Kind, true) {
			return fmt.Errorf("方法 %q 的参数 %q 类型 %q 不认识", name, spec.Name, spec.Kind)
		}
		// 必填排在可选之后 = 读描述的人无从判断"前面那个到底能不能省"。
		if spec.Required && sawOptional {
			return fmt.Errorf("方法 %q 的参数 %q 是必填却排在可选参数之后", name, spec.Name)
		}
		if !spec.Required {
			sawOptional = true
		}
		if len(spec.Enum) > 0 && spec.Kind != ParamString {
			return fmt.Errorf("方法 %q 的参数 %q：enum 只对 string 有意义", name, spec.Name)
		}
		if spec.MinLength < 0 || spec.MaxLength < 0 {
			return fmt.Errorf("方法 %q 的参数 %q：长度不能为负", name, spec.Name)
		}
		if spec.MaxLength > 0 && spec.MinLength > spec.MaxLength {
			return fmt.Errorf("方法 %q 的参数 %q：minLength %d > maxLength %d", name, spec.Name, spec.MinLength, spec.MaxLength)
		}
		if spec.Kind == ParamArray {
			if spec.Items == "" {
				return fmt.Errorf("方法 %q 的参数 %q 是数组但没有 items（元素类型不明）", name, spec.Name)
			}
			if !validParamKind(spec.Items, false) {
				return fmt.Errorf("方法 %q 的参数 %q：items 类型 %q 不认识", name, spec.Name, spec.Items)
			}
		} else if spec.Items != "" {
			return fmt.Errorf("方法 %q 的参数 %q：只有 array 能声明 items", name, spec.Name)
		}
	}
	return nil
}

func validParamKind(kind ParamKind, allowAny bool) bool {
	switch kind {
	case ParamString, ParamBool, ParamNumber, ParamInteger, ParamObject, ParamArray:
		return true
	case ParamAny:
		return allowAny
	default:
		return false
	}
}

// CanonicalDescriptors 是 canonical 25 的描述表。
//
// `required` 故意一律 false：这是**描述**而不是**门禁**。逐方法的必填判定必须
// 对照 handler 才能打开（例如 session.load 同时认 id 与 sessionId，硬性要求
// 其中一个就会把另一种合法客户端拦在门外）。Lead 决定哪些方法接校验。
func CanonicalDescriptors() []Descriptor {
	return []Descriptor{
		{Method: MethodSessionList, Summary: "列当前 home 下的会话", Source: "canonical"},
		{Method: MethodSessionLoad, Summary: "加载一个会话的首屏投影", Source: "canonical", Params: []ParamSpec{
			{Name: "id", Kind: ParamString}, {Name: "sessionId", Kind: ParamString},
		}},
		{Method: MethodSessionHistory, Summary: "按 journal 取会话历史帧", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "since", Kind: ParamInteger}, {Name: "limit", Kind: ParamInteger},
		}},
		{Method: MethodSessionPrompt, Summary: "发一条用户消息", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "text", Kind: ParamString},
		}},
		{Method: MethodSessionCancel, Summary: "取消在飞的一轮", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString},
		}},
		{Method: MethodSessionApprove, Summary: "回一次审批", Source: "canonical", Params: []ParamSpec{
			{Name: "requestId", Kind: ParamString}, {Name: "optionId", Kind: ParamString},
		}},
		{Method: MethodSessionCreate, Summary: "新建会话", Source: "canonical", Params: []ParamSpec{
			{Name: "mode", Kind: ParamString},
		}},
		{Method: MethodSessionDelete, Summary: "删除会话", Source: "canonical", Params: []ParamSpec{
			{Name: "id", Kind: ParamString}, {Name: "sessionId", Kind: ParamString},
		}},
		{Method: MethodSessionFork, Summary: "从某轮 fork 会话", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "anchor", Kind: ParamString}, {Name: "title", Kind: ParamString},
		}},
		{Method: MethodSessionCompact, Summary: "压一次会话上下文", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString},
		}},
		{Method: MethodWorkspaceList, Summary: "列工作区目录条目", Source: "canonical", Params: []ParamSpec{
			{Name: "path", Kind: ParamString},
		}},
		{Method: MethodJobsList, Summary: "列后台任务", Source: "canonical"},
		{Method: MethodJobsOutput, Summary: "读后台任务输出", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "id", Kind: ParamString},
			{Name: "cursor", Kind: ParamInteger}, {Name: "limit", Kind: ParamInteger}, {Name: "wait", Kind: ParamNumber},
		}},
		{Method: MethodScheduleList, Summary: "列定时任务", Source: "canonical"},
		{Method: MethodSessionSetModel, Summary: "设置会话的模型覆盖", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "model", Kind: ParamString},
		}},
		{Method: MethodSessionSetPermission, Summary: "设置会话的审批策略覆盖", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "permission", Kind: ParamString},
		}},
		{Method: MethodModelsList, Summary: "列网关上可用的模型", Source: "canonical"},
		{Method: MethodSessionSetGoal, Summary: "设置或清除会话 goal", Source: "canonical", Params: []ParamSpec{
			{Name: "sessionId", Kind: ParamString}, {Name: "goal", Kind: ParamString},
		}},
		{Method: MethodTerminalOpen, Summary: "打开一条长连接终端", Source: "canonical", Params: []ParamSpec{
			{Name: "cwd", Kind: ParamString},
		}},
		{Method: MethodTerminalInput, Summary: "往终端写输入", Source: "canonical", Params: []ParamSpec{
			{Name: "id", Kind: ParamString}, {Name: "data", Kind: ParamString},
		}},
		{Method: MethodTerminalOutput, Summary: "读终端输出", Source: "canonical", Params: []ParamSpec{
			{Name: "id", Kind: ParamString}, {Name: "since", Kind: ParamInteger},
		}},
		{Method: MethodFSRead, Summary: "读一个文件", Source: "canonical", Params: []ParamSpec{
			{Name: "path", Kind: ParamString},
		}},
		{Method: MethodFSWrite, Summary: "写一个文件", Source: "canonical", Params: []ParamSpec{
			{Name: "path", Kind: ParamString}, {Name: "content", Kind: ParamString},
		}},
		{Method: MethodConfigGet, Summary: "取整份界面配置（扁平投影，旧协议）", Source: "canonical"},
		{Method: MethodConfigSet, Summary: "改若干项界面配置（扁平键值，旧协议）", Source: "canonical", Params: []ParamSpec{
			{Name: "values", Kind: ParamObject},
		}},
	}
}

// DescriptorSnapshot 是给 Lead 审查的官方方法快照：canonical 25 + 本任务新增的
// additive 面。字节稳定（同一份代码永远同样输出），可以直接进 golden。
func DescriptorSnapshot() ([]byte, error) {
	all := make([]Descriptor, 0, len(SessionMethods())+len(ConfigPatchMethods()))
	all = append(all, CanonicalDescriptors()...)
	all = append(all, ConfigPatchDescriptors()...)
	sort.Slice(all, func(i, j int) bool { return all[i].Method < all[j].Method })
	return marshalDescriptorDoc(all)
}

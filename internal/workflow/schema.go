package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// 这一文件是官方 `dsh-tools` 的 JSON Schema 子集在宿主侧的移植
// （`@deepseek-ai/dsh-tools` 的 `json-schema.ts`，rc.2）。
//
// 为什么要子集而不是"把 schema 原样透传"：官方 `agent()` 的 `schema` 是
// **受强制执行的**——不在子集里就当场 `UNSUPPORTED_SCHEMA`，而不是收下之后
// 什么都不校验（那会让模型以为"结构化输出生效了"）。同样的判据在工具输出、
// PTC 类型生成、子代理结构化输出上共用（官方注释：`Enforced JSON Schema subset
// shared by tool outputs, generated PTC mode types, subagents, and workflows`）。
//
// 官方子集（逐条）：
//   - 任意 JSON 根；**工作流/子代理要求根是 object**；
//   - 一个标量 type，或恰好一个 oneOf；
//   - object 的 properties / required / boolean additionalProperties；
//   - array 的 items；
//   - 标量 enum / const（类型必须与 type 一致）；
//   - 注解 description / title / default / examples；
//   - 其他关键字一律**拒绝**，不做"接受但不校验"。

// schemaConstraintKeywords 是子集里的约束关键字（官方 json-schema.ts 的同名集合）。
var schemaConstraintKeywords = map[string]bool{
	"type": true, "oneOf": true, "properties": true, "required": true,
	"additionalProperties": true, "items": true, "enum": true, "const": true,
}

// schemaAnnotationKeywords 是子集里的注解关键字。
var schemaAnnotationKeywords = map[string]bool{
	"description": true, "title": true, "default": true, "examples": true,
}

// schemaTypes 是允许的 type 值。
var schemaTypes = map[string]bool{
	"object": true, "array": true, "string": true, "number": true,
	"integer": true, "boolean": true, "null": true,
}

// oneOfSiblingKeywords 是"与 oneOf 并列就非法"的约束关键字（官方同名单）。
var oneOfSiblingKeywords = []string{
	"properties", "required", "additionalProperties", "items", "enum", "const",
}

// schemaMaxDepth 是 schema 树的深度上限。
//
// 官方用显式栈遍历、没有上限（JS 的对象图天然有环检测）。Go 这边是递归，
// 给一个上限把"恶意深嵌套 schema"挡在栈溢出之前——超限按子集外处理。
const schemaMaxDepth = 64

// AssertObjectJSONSchema 校验 schema 属于受支持子集且根是 object
// （官方 `assertObjectJsonSchema`）。
//
// 失败返回 `*Error{Code: CodeUnsupportedSchema}`，消息里逐条列出违规路径
// （官方 `JsonSchemaError` 的 `violations`），让模型一次改对。
func AssertObjectJSONSchema(schema any) error {
	var violations []string
	violations = append(violations, checkSchemaNode(schema, "schema", 0)...)
	if len(violations) == 0 {
		record, ok := schema.(map[string]any)
		if !ok || record["type"] != "object" {
			violations = append(violations,
				`schema.type must be "object" (structured output is object-rooted)`)
		}
	}
	if len(violations) > 0 {
		return NewError(CodeUnsupportedSchema,
			"agent() schema is outside the supported subset — unsupported JSON schema: "+
				strings.Join(violations, "; "))
	}
	return nil
}

// checkSchemaNode 递归校验一个 schema 节点，返回全部违规（不早停：官方同样
// "一次报全"，模型改一次就好）。
func checkSchemaNode(node any, path string, depth int) []string {
	if depth > schemaMaxDepth {
		return []string{path + " is nested too deeply for the supported subset"}
	}
	record, ok := node.(map[string]any)
	if !ok {
		return []string{path + " must be a schema object"}
	}
	var violations []string
	for _, key := range sortedKeys(record) {
		if schemaConstraintKeywords[key] || schemaAnnotationKeywords[key] {
			continue
		}
		violations = append(violations, fmt.Sprintf(
			"%s.%s is not a supported keyword (subset: type/oneOf/properties/required/additionalProperties/items/enum/const + annotations)",
			path, key))
	}
	if v, present := record["description"]; present {
		if _, ok := v.(string); !ok {
			violations = append(violations, path+".description must be a string")
		}
	}
	if v, present := record["title"]; present {
		if _, ok := v.(string); !ok {
			violations = append(violations, path+".title must be a string")
		}
	}
	typeValue, hasType := record["type"]
	oneOf, hasOneOf := record["oneOf"]
	if hasType && hasOneOf {
		return append(violations, path+" cannot declare both type and oneOf")
	}
	if !hasType && !hasOneOf {
		for _, key := range oneOfSiblingKeywords {
			if _, present := record[key]; present {
				violations = append(violations, path+"."+key+" requires type or oneOf")
			}
		}
		return violations
	}
	if hasOneOf {
		list, ok := oneOf.([]any)
		if !ok || len(list) < 2 {
			violations = append(violations, path+".oneOf must be an array of at least two schemas")
		} else {
			for index := len(list) - 1; index >= 0; index-- {
				violations = append(violations,
					checkSchemaNode(list[index], fmt.Sprintf("%s.oneOf[%d]", path, index), depth+1)...)
			}
		}
		for _, key := range oneOfSiblingKeywords {
			if _, present := record[key]; present {
				violations = append(violations, path+"."+key+" is not supported beside oneOf")
			}
		}
		return violations
	}
	schemaType, ok := typeValue.(string)
	if !ok || !schemaTypes[schemaType] {
		if _, isList := typeValue.([]any); isList {
			return append(violations, path+".type must be a single type string (type arrays are not supported)")
		}
		return append(violations, fmt.Sprintf(
			"%s.type must be one of object/array/string/number/integer/boolean/null", path))
	}
	// 关键字与类型的搭配（官方 json-schema.ts 的同名表）。
	allowed := map[string][]string{
		"properties":           {"object"},
		"required":             {"object"},
		"additionalProperties": {"object"},
		"items":                {"array"},
		"enum":                 {"string", "number", "integer", "boolean", "null"},
		"const":                {"string", "number", "integer", "boolean", "null"},
	}
	for key, types := range allowed {
		if _, present := record[key]; !present {
			continue
		}
		compatible := false
		for _, t := range types {
			if t == schemaType {
				compatible = true
				break
			}
		}
		if !compatible {
			violations = append(violations, fmt.Sprintf("%s.%s is not supported on type %q", path, key, schemaType))
		}
	}
	switch schemaType {
	case "object":
		violations = append(violations, checkObjectSchema(record, path, depth)...)
	case "array":
		if items, present := record["items"]; present {
			violations = append(violations, checkSchemaNode(items, path+".items", depth+1)...)
		}
	case "string", "number", "integer", "boolean", "null":
		violations = append(violations, checkScalarSchema(record, path, schemaType)...)
	}
	return violations
}

// checkObjectSchema 校验 object 节点的 properties / required / additionalProperties。
func checkObjectSchema(record map[string]any, path string, depth int) []string {
	var violations []string
	properties, hasProperties := record["properties"]
	if hasProperties {
		props, ok := properties.(map[string]any)
		if !ok {
			violations = append(violations, path+".properties must be an object of schemas")
		} else {
			for _, key := range sortedKeys(props) {
				violations = append(violations,
					checkSchemaNode(props[key], path+".properties."+key, depth+1)...)
			}
		}
	}
	required, hasRequired := record["required"]
	if hasRequired {
		list, ok := required.([]any)
		if !ok {
			violations = append(violations, path+".required must be an array of strings")
		} else {
			declared, _ := properties.(map[string]any)
			for _, entry := range list {
				name, isString := entry.(string)
				if !isString {
					violations = append(violations, path+".required must be an array of strings")
					break
				}
				if _, present := declared[name]; !present {
					violations = append(violations,
						fmt.Sprintf("%s.required names %q which is not in properties", path, name))
				}
			}
		}
	}
	if additional, present := record["additionalProperties"]; present {
		if _, ok := additional.(bool); !ok {
			violations = append(violations, path+".additionalProperties must be a boolean")
		}
	}
	return violations
}

// checkScalarSchema 校验标量节点的 enum / const。
func checkScalarSchema(record map[string]any, path, schemaType string) []string {
	var violations []string
	enum, hasEnum := record["enum"]
	enumValid := false
	var allowed []any
	if hasEnum {
		if list, ok := enum.([]any); ok && len(list) > 0 {
			enumValid = true
			allowed = list
			for _, entry := range list {
				if !scalarMatches(schemaType, entry) {
					enumValid = false
					break
				}
			}
		}
		if !enumValid {
			violations = append(violations,
				fmt.Sprintf("%s.enum must be a non-empty array of %s values", path, schemaType))
		}
	}
	if declared, present := record["const"]; present {
		if !scalarMatches(schemaType, declared) {
			violations = append(violations,
				fmt.Sprintf("%s.const must be a %s value", path, schemaType))
		} else if enumValid && !containsValue(allowed, declared) {
			violations = append(violations,
				fmt.Sprintf("%s.const must be one of %s.enum when both are declared", path, path))
		}
	}
	return violations
}

// scalarMatches 报告值是否匹配一个标量类型（官方 `scalarMatches`）。
func scalarMatches(schemaType string, value any) bool {
	switch schemaType {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case float64, float32, int, int64, int32:
			return true
		}
		return false
	case "integer":
		switch v := value.(type) {
		case float64:
			return v == float64(int64(v))
		case int, int64, int32:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	}
	return false
}

// containsValue 用深比较判断 value 是否在 allowed 里（JSON 值的相等）。
func containsValue(allowed []any, value any) bool {
	return jsonEqual(any(allowed), []any{value}) == nil
}

// ValidateAgainstSchema 用一个**已通过 AssertObjectJSONSchema** 的 schema 校验值，
// 返回全部违规路径（空 = 通过）。
//
// 这一层是 `agent()` 结构化输出的强制执行：官方把 schema 交给 provider 的
// `outputSchema` 由 provider 保证；本宿主的子代理返回的是正文，所以由引擎按同一份
// 子集**自己校验**——收下不符合的结果等于把 schema 当装饰。
func ValidateAgainstSchema(schema any, value any) []string {
	return validateValue(schema, value, "value", 0)
}

func validateValue(schema any, value any, path string, depth int) []string {
	if depth > schemaMaxDepth {
		return []string{path + " is nested too deeply"}
	}
	record, ok := schema.(map[string]any)
	if !ok {
		// 注解-only schema（没有 type/oneOf）接受任意 JSON。
		return nil
	}
	if oneOf, present := record["oneOf"]; present {
		if list, ok := oneOf.([]any); ok {
			matched := 0
			for _, candidate := range list {
				if len(validateValue(candidate, value, path, depth+1)) == 0 {
					matched++
				}
			}
			if matched != 1 {
				return []string{fmt.Sprintf("%s matches %d of the oneOf branches (exactly one required)", path, matched)}
			}
			return nil
		}
	}
	schemaType, ok := record["type"].(string)
	if !ok {
		return nil
	}
	var violations []string
	switch schemaType {
	case "object":
		object, isObject := value.(map[string]any)
		if !isObject {
			return []string{fmt.Sprintf("%s must be an object", path)}
		}
		properties, _ := record["properties"].(map[string]any)
		required, _ := record["required"].([]any)
		for _, entry := range required {
			name, _ := entry.(string)
			if _, present := object[name]; !present {
				violations = append(violations, fmt.Sprintf("%s.%s is required", path, name))
			}
		}
		if additional, present := record["additionalProperties"]; present {
			if allow, isBool := additional.(bool); isBool && !allow {
				for _, key := range sortedKeys(object) {
					if _, declared := properties[key]; !declared {
						violations = append(violations, fmt.Sprintf("%s.%s is not allowed (additionalProperties: false)", path, key))
					}
				}
			}
		}
		for _, key := range sortedKeys(object) {
			propertySchema, declared := properties[key]
			if !declared {
				continue
			}
			violations = append(violations, validateValue(propertySchema, object[key], path+"."+key, depth+1)...)
		}
	case "array":
		list, isList := value.([]any)
		if !isList {
			return []string{fmt.Sprintf("%s must be an array", path)}
		}
		if items, present := record["items"]; present {
			for index, entry := range list {
				violations = append(violations,
					validateValue(items, entry, fmt.Sprintf("%s[%d]", path, index), depth+1)...)
			}
		}
	case "string", "number", "integer", "boolean", "null":
		if !scalarMatches(schemaType, value) {
			return []string{fmt.Sprintf("%s must be a %s", path, schemaType)}
		}
		if allowed, present := record["enum"].([]any); present && !containsValue(allowed, value) {
			violations = append(violations, fmt.Sprintf("%s must be one of the enum values", path))
		}
		if declared, present := record["const"]; present && jsonEqual(declared, value) != nil {
			violations = append(violations, fmt.Sprintf("%s must equal const", path))
		}
	}
	return violations
}

// jsonEqual 用 encoding/json 的语义深比较两个值；不等返回错误。
//
// 为什么借道 JSON 而不是自己写递归比较：schema 值的相等必须与"落盘/跨进程"
// 之后的相等一致（数字 1 与 1.0、对象键序），自己写的比较器迟早会与 JSON 语义分叉。
func jsonEqual(left, right any) error {
	leftRaw, err := json.Marshal(left)
	if err != nil {
		return err
	}
	rightRaw, err := json.Marshal(right)
	if err != nil {
		return err
	}
	if !bytes.Equal(leftRaw, rightRaw) {
		return fmt.Errorf("values differ")
	}
	return nil
}

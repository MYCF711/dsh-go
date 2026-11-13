package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// metaKnownFields 是 meta 允许的字段（官方 `dsh-workflow-ptc/src/meta.ts:19`
// 的 `known` 集合）。多一个键就是 META_INVALID：官方按白名单拒绝未知字段，
// 不是"忽略不认识的"。
var metaKnownFields = map[string]bool{
	"name":        true,
	"description": true,
	"whenToUse":   true,
	"phases":      true,
}

// phaseKnownFields 是 meta.phases[i] 允许的字段（官方 meta.ts:38）。
var phaseKnownFields = map[string]bool{
	"title":    true,
	"detail":   true,
	"provider": true,
	"model":    true,
}

// ValidateMeta 把调用方给的**数据**校验成 Meta（官方 `validateMeta`，meta.ts:76-82）。
//
// 契约要点（逐条对齐 meta.ts:13-65）：
//   - meta 必须是对象（不是数组、不是 null）；
//   - 字段白名单 name/description/whenToUse/phases，未知字段逐条报名字；
//   - name / description 必须是非空字符串；
//   - whenToUse 若在，必须是字符串；
//   - phases 若在，必须是数组；每个元素必须是对象，按
//     title/detail/provider/model 再校验一遍；
//   - **一次性列出全部违规**（官方是"collect violations then throw"），
//     因为模型改一次要一次往返，只报第一条会让它改 N 轮。
//
// 返回值是**归一化副本**：引擎不持有调用方的 map（官方 :68-71 的理由：
// 永远是 caller 数据的只读拷贝，不能是别名）。
func ValidateMeta(raw any) (Meta, error) {
	meta, violations := validateMetaShape(raw)
	if len(violations) > 0 {
		return Meta{}, NewError(CodeMetaInvalid, "invalid meta: "+strings.Join(violations, "; "))
	}
	return meta, nil
}

// validateMetaShape 收集形状违规并尽力产出归一化 meta。
func validateMetaShape(raw any) (Meta, []string) {
	record, ok := raw.(map[string]any)
	if !ok {
		// 用 %T 而不是 %v：值为 nil 时 %v 是 "<nil>"，看不出是"没给"还是"给了 nil"。
		if raw == nil {
			return Meta{}, []string{"meta must be an object"}
		}
		return Meta{}, []string{"meta must be an object"}
	}
	var violations []string
	// 未知字段按排序报，保证同一份输入永远得到同一句话（map 迭代序随机，
	// 随机错误消息会让"同一故障两次报不同原因"变成一类查不出来的噪声）。
	for _, key := range sortedKeys(record) {
		if !metaKnownFields[key] {
			violations = append(violations,
				fmt.Sprintf("meta.%s is not a recognized field (name/description/whenToUse/phases)", key))
		}
	}
	name, nameOK := record["name"].(string)
	if !nameOK || name == "" {
		violations = append(violations, "meta.name must be a non-empty string")
	}
	description, descOK := record["description"].(string)
	if !descOK || description == "" {
		violations = append(violations, "meta.description must be a non-empty string")
	}
	whenToUse := ""
	if v, present := record["whenToUse"]; present {
		s, ok := v.(string)
		if !ok {
			violations = append(violations, "meta.whenToUse must be a string")
		} else {
			whenToUse = s
		}
	}
	var phases []Phase
	if v, present := record["phases"]; present {
		list, ok := v.([]any)
		if !ok {
			violations = append(violations, "meta.phases must be an array")
		} else {
			for index, entry := range list {
				phase, phaseViolations := validatePhase(entry, index)
				violations = append(violations, phaseViolations...)
				if len(phaseViolations) == 0 {
					phases = append(phases, phase)
				}
			}
		}
	}
	if len(violations) > 0 {
		return Meta{}, violations
	}
	return Meta{
		Name:        name,
		Description: description,
		WhenToUse:   whenToUse,
		Phases:      phases,
	}, nil
}

// validatePhase 校验 meta.phases[i]（官方 meta.ts:31-53）。
func validatePhase(raw any, index int) (Phase, []string) {
	entry, ok := raw.(map[string]any)
	if !ok {
		return Phase{}, []string{fmt.Sprintf("meta.phases[%d] must be an object", index)}
	}
	var violations []string
	for _, key := range sortedKeys(entry) {
		if !phaseKnownFields[key] {
			violations = append(violations,
				fmt.Sprintf("meta.phases[%d].%s is not a recognized field", index, key))
		}
	}
	phase := Phase{}
	if v, present := entry["title"]; present {
		title, ok := v.(string)
		if !ok || title == "" {
			violations = append(violations,
				fmt.Sprintf("meta.phases[%d].title must be a non-empty string", index))
		} else {
			phase.Title = title
		}
	} else {
		violations = append(violations,
			fmt.Sprintf("meta.phases[%d].title must be a non-empty string", index))
	}
	for _, field := range []string{"detail", "provider", "model"} {
		v, present := entry[field]
		if !present {
			continue
		}
		s, ok := v.(string)
		if !ok {
			violations = append(violations,
				fmt.Sprintf("meta.phases[%d].%s must be a string", index, field))
			continue
		}
		switch field {
		case "detail":
			phase.Detail = s
		case "provider":
			phase.Provider = s
		case "model":
			phase.Model = s
		}
	}
	return phase, violations
}

// sortedKeys 返回 map 的键（升序）。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

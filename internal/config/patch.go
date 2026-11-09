package config

// 补丁：一次调用要么把这一批 op 全部应用，要么一条都不应用。
//
// 为什么不是"逐条尽力而为"：配置是**整体**被读的（界面拿到的是整棵树）。
// 如果 3 条 op 里只有 2 条成功，用户看到的树既不是改前也不是改后，
// 而且没有任何一处能告诉他"中间那条失败了"。所以先全部校验、再在拷贝上应用，
// 出错就丢掉拷贝（基准树一个字节都不动，有测试钉住）。

import (
	"errors"
	"fmt"
)

// Op 是补丁动作。字符串就是线协议上的值。
type Op string

const (
	// OpSet 把路径写到给定值（中间层不存在就建）。
	OpSet Op = "set"
	// OpUnset 删掉路径。路径不存在是**错误**，不是静默成功。
	OpUnset Op = "unset"
	// OpMerge 把一个对象合并进目标对象（浅合并：一个 key 一个值，
	// 嵌套对象整体替换——深合并会让"删掉某个子键"无法表达）。
	OpMerge Op = "merge"
)

const (
	// MaxOps 是一次补丁的条数上限。配置是人改的，一次 256 条以上一定是脚本出错了；
	// 有界也让"一次请求占着锁做几万次写"不可能发生。
	MaxOps = 256
)

var (
	// ErrUnknownOp 是"这个 op 不认识"。
	ErrUnknownOp = errors.New("未知的补丁动作")
	// ErrInvalidPath 是"路径不合法"（空、空段、超深）。
	ErrInvalidPath = errors.New("补丁路径不合法")
	// ErrNoSuchPath 是"要删的路径不存在"。
	ErrNoSuchPath = errors.New("补丁路径不存在")
	// ErrNotObject 是"要在标量上继续往下走"。
	ErrNotObject = errors.New("路径中间不是对象")
)

// PatchOp 是一条补丁动作。
type PatchOp struct {
	Op    Op     `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// Patch 是一次原子写。ExpectedRevision 是**必须**给的：客户端先读到 N，
// 再带 N 回来写；服务端当前不是 N 就冲突（`*RevisionConflict`）。
type Patch struct {
	ExpectedRevision int64     `json:"expectedRevision"`
	Ops              []PatchOp `json:"ops"`
	Author           string    `json:"author,omitempty"`
}

// Validate 只做形状校验（不碰任何树）。
func (p Patch) Validate() error {
	if len(p.Ops) == 0 {
		return fmt.Errorf("%w：一次补丁至少要有一条 op（空调用不该\"成功\"）", ErrInvalidValue)
	}
	if len(p.Ops) > MaxOps {
		return fmt.Errorf("%w：一次最多 %d 条 op，拿到 %d 条", ErrInvalidValue, MaxOps, len(p.Ops))
	}
	for index, op := range p.Ops {
		switch op.Op {
		case OpSet, OpUnset, OpMerge:
		default:
			return fmt.Errorf("第 %d 条：%w %q（只认 set/unset/merge）", index+1, ErrUnknownOp, op.Op)
		}
		if _, err := splitPath(op.Path); err != nil {
			return fmt.Errorf("第 %d 条：%w", index+1, err)
		}
	}
	return nil
}

// ApplyPatch 在 base 的**拷贝**上应用整批 op，返回新树。
//
// base 永远不被修改（包括失败路径）。base 为 nil 时当作空树。
func ApplyPatch(base *Tree, patch Patch) (*Tree, error) {
	if base == nil {
		base = EmptyTree()
	}
	if err := patch.Validate(); err != nil {
		return nil, err
	}

	root := deepCopyObject(base.root)
	for index, op := range patch.Ops {
		if err := applyOp(root, op); err != nil {
			return nil, fmt.Errorf("第 %d 条 op（%s %s）失败，整批未应用：%w", index+1, op.Op, op.Path, err)
		}
	}
	return &Tree{root: root}, nil
}

func applyOp(root map[string]any, op PatchOp) error {
	segments, err := splitPath(op.Path)
	if err != nil {
		return err
	}
	switch op.Op {
	case OpSet:
		value, err := normalizeValue(op.Value, 0)
		if err != nil {
			return err
		}
		return setPath(root, segments, value)
	case OpUnset:
		return unsetPath(root, segments)
	case OpMerge:
		value, err := normalizeValue(op.Value, 0)
		if err != nil {
			return err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%w：merge 的值必须是对象，拿到 %T", ErrInvalidValue, op.Value)
		}
		return mergePath(root, segments, object)
	default:
		return fmt.Errorf("%w %q", ErrUnknownOp, op.Op)
	}
}

func setPath(root map[string]any, segments []string, value any) error {
	parent, err := descend(root, segments[:len(segments)-1], true)
	if err != nil {
		return err
	}
	parent[segments[len(segments)-1]] = value
	return nil
}

func unsetPath(root map[string]any, segments []string) error {
	parent, err := descend(root, segments[:len(segments)-1], false)
	if err != nil {
		return err
	}
	key := segments[len(segments)-1]
	if _, ok := parent[key]; !ok {
		return fmt.Errorf("%w：%s", ErrNoSuchPath, joinPath(segments))
	}
	delete(parent, key)
	return nil
}

func mergePath(root map[string]any, segments []string, value map[string]any) error {
	parent, err := descend(root, segments[:len(segments)-1], true)
	if err != nil {
		return err
	}
	key := segments[len(segments)-1]
	switch existing := parent[key].(type) {
	case nil:
		parent[key] = value
		return nil
	case map[string]any:
		for child, childValue := range value {
			existing[child] = childValue
		}
		return nil
	default:
		return fmt.Errorf("%w：%s 当前是 %T，不能 merge 对象", ErrNotObject, joinPath(segments), parent[key])
	}
}

// descend 走到倒数第二层。create=true 时缺的中间层建空对象；
// create=false 时缺层就是 ErrNoSuchPath（unset 不许凭空造路径）。
func descend(root map[string]any, segments []string, create bool) (map[string]any, error) {
	node := root
	for index, segment := range segments {
		value, ok := node[segment]
		if !ok {
			if !create {
				return nil, fmt.Errorf("%w：%s", ErrNoSuchPath, joinPath(segments[:index+1]))
			}
			child := map[string]any{}
			node[segment] = child
			node = child
			continue
		}
		child, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w：%s 是 %T", ErrNotObject, joinPath(segments[:index+1]), value)
		}
		node = child
	}
	return node, nil
}

func joinPath(segments []string) string {
	out := ""
	for index, segment := range segments {
		if index > 0 {
			out += "."
		}
		out += segment
	}
	return out
}

package drusql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// rule 是 validate 规则链中的一条，写法与 Laravel 验证器一致：name:arg1,arg2。
type rule struct {
	name string
	args []string
	nums []float64      // min / max / between 的参数
	re   *regexp.Regexp // regex 的参数
}

var ruleAliases = map[string]string{"integer": "int", "boolean": "bool"}

// parseRules 解析 "required|string|max:100" 形式的规则串。
// regex 的模式里可能含有 |，所以 regex 必须写在最后，它会吃掉剩余的全部内容。
func parseRules(s string) ([]rule, error) {
	var rules []rule
	rest := s
	for rest != "" {
		var part string
		if strings.HasPrefix(rest, "regex:") {
			part, rest = rest, ""
		} else if i := strings.IndexByte(rest, '|'); i >= 0 {
			part, rest = rest[:i], rest[i+1:]
		} else {
			part, rest = rest, ""
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, argStr, hasArgs := strings.Cut(part, ":")
		name = strings.TrimSpace(name)
		if alias, ok := ruleAliases[name]; ok {
			name = alias
		}
		r := rule{name: name}
		switch name {
		case "required", "string", "int", "numeric", "bool", "array", "date":
			if hasArgs {
				return nil, fmt.Errorf("rule %q takes no arguments", name)
			}
		case "min", "max", "between":
			want := 1
			if name == "between" {
				want = 2
			}
			r.args = splitArgs(argStr)
			if !hasArgs || len(r.args) != want {
				return nil, fmt.Errorf("rule %q expects %d argument(s)", name, want)
			}
			for _, a := range r.args {
				f, err := strconv.ParseFloat(a, 64)
				if err != nil {
					return nil, fmt.Errorf("rule %q: %q is not a number", name, a)
				}
				r.nums = append(r.nums, f)
			}
		case "in":
			r.args = splitArgs(argStr)
			if !hasArgs || len(r.args) == 0 {
				return nil, fmt.Errorf("rule \"in\" expects at least one value")
			}
		case "regex":
			if !hasArgs || argStr == "" {
				return nil, fmt.Errorf("rule \"regex\" expects a pattern")
			}
			pattern := argStr
			// 兼容 Laravel 的 /pattern/ 写法
			if len(pattern) >= 2 && pattern[0] == '/' && pattern[len(pattern)-1] == '/' {
				pattern = pattern[1 : len(pattern)-1]
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("rule \"regex\": %s", err.Error())
			}
			r.re = re
		default:
			return nil, fmt.Errorf("unknown validation rule %q", name)
		}
		rules = append(rules, r)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("rule is empty")
	}
	return rules, nil
}

func splitArgs(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

var intPattern = regexp.MustCompile(`^-?\d+$`)

// firstFailure 返回规则链中第一条没通过的规则名；全部通过返回 ""。
// 值为 undefined、null 或空字符串时，除 required 外的规则都跳过。
func firstFailure(rules []rule, v any) string {
	numeric := false
	for _, r := range rules {
		if r.name == "int" || r.name == "numeric" {
			numeric = true
		}
	}
	skip := absent(v) || v == ""
	for _, r := range rules {
		if r.name == "required" {
			if isEmpty(v) {
				return r.name
			}
			continue
		}
		if skip {
			continue
		}
		if !rulePasses(r, v, numeric) {
			return r.name
		}
	}
	return ""
}

func rulePasses(r rule, v any, numeric bool) bool {
	switch r.name {
	case "string":
		_, ok := v.(string)
		return ok
	case "int":
		switch x := v.(type) {
		case int64:
			return true
		case string:
			return intPattern.MatchString(x)
		}
		return false
	case "numeric":
		_, ok := looseFloat(v)
		return ok
	case "bool":
		switch x := v.(type) {
		case bool:
			return true
		case int64:
			return x == 0 || x == 1
		case string:
			return x == "0" || x == "1"
		}
		return false
	case "array":
		_, ok := v.([]any)
		return ok
	case "date":
		if _, ok := v.(time.Time); ok {
			return true
		}
		if _, ok := v.(string); !ok {
			return false
		}
		_, ok := parseDate(v)
		return ok
	case "min":
		size, ok := sizeOf(v, numeric)
		return ok && size >= r.nums[0]
	case "max":
		size, ok := sizeOf(v, numeric)
		return ok && size <= r.nums[0]
	case "between":
		size, ok := sizeOf(v, numeric)
		return ok && size >= r.nums[0] && size <= r.nums[1]
	case "in":
		s, ok := toStr(v)
		if !ok {
			return false
		}
		for _, a := range r.args {
			if a == s {
				return true
			}
		}
		return false
	case "regex":
		s, ok := toStr(v)
		return ok && r.re.MatchString(s)
	}
	return false
}

// sizeOf 与 Laravel 一致：数字按大小，字符串按字符数，数组按元素个数。
// 规则链里有 int / numeric 时，数字形式的字符串也按数值算。
func sizeOf(v any, numeric bool) (float64, bool) {
	switch x := v.(type) {
	case int64, float64:
		return toFloat(x)
	case string:
		if numeric {
			if f, ok := looseFloat(x); ok {
				return f, true
			}
		}
		return float64(runeLen(x)), true
	case []any:
		return float64(len(x)), true
	case map[string]any:
		return float64(len(x)), true
	}
	return 0, false
}

// expandValidatePath 求出 validate 路径对应的全部（具体路径，值）。
// [*] 按父数组的下标展开；取不到的成员是 Undefined，这样 required 能检出缺失字段。
func expandValidatePath(pe *pathExpr, root any) []pathNode {
	list := []pathNode{{root, "$"}}
	for _, seg := range pe.segs {
		sel := seg.selectors[0]
		var next []pathNode
		for _, n := range list {
			switch sel.kind {
			case selName:
				var child any = Undefined
				if m, ok := n.val.(map[string]any); ok {
					if v, ok := m[sel.name]; ok {
						child = v
					}
				}
				next = append(next, pathNode{child, childPath(n.path, sel.name)})
			case selIndex:
				var child any = Undefined
				if a, ok := n.val.([]any); ok && sel.index < len(a) {
					child = a[sel.index]
				}
				next = append(next, pathNode{child, indexPath(n.path, sel.index)})
			case selWildcard:
				if a, ok := n.val.([]any); ok {
					for i, e := range a {
						next = append(next, pathNode{e, indexPath(n.path, i)})
					}
				}
			}
		}
		list = next
	}
	return list
}

// runValidation 是执行的第一步：按书写顺序执行顶层的 set 和 validate，
// 校验失败后不停止，收集全部失败项。
func (c *execCtx) runValidation() (ValidationErrors, error) {
	var errs ValidationErrors
	for _, n := range c.tpl.nodes {
		switch x := n.(type) {
		case *setNode:
			if err := c.execSet(x); err != nil {
				return nil, err
			}
		case *validateNode:
			for _, target := range expandValidatePath(x.path, c.root) {
				failed := firstFailure(x.rules, target.val)
				if failed == "" {
					continue
				}
				msg, err := c.validateMessage(x, target.path, failed)
				if err != nil {
					return nil, err
				}
				errs = append(errs, ValidationError{Field: target.path, Rule: failed, Message: msg})
			}
		}
	}
	return errs, nil
}

func (c *execCtx) validateMessage(n *validateNode, field, failed string) (string, error) {
	if n.msg == nil {
		return field + " failed the " + failed + " rule", nil
	}
	v, err := c.eval(n.msg)
	if err != nil {
		return "", err
	}
	if s, ok := toStr(v); ok {
		return s, nil
	}
	return "", c.errAt(n.pos, "validate message must be a string, got %s", typeName(v))
}

package drusql

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type funcDef struct {
	min, max int // max 为 -1 表示不限
	fn       func(c *execCtx, args []any) (any, error)
}

// builtins 是内置函数表。管道 x|f(a) 等价于 f(x, a)。
var builtins map[string]funcDef

func init() {
	builtins = map[string]funcDef{
		// 通用
		"default": {2, 2, fnDefault},
		"trim":    {1, 1, strFunc(strings.TrimSpace)},
		"lower":   {1, 1, strFunc(strings.ToLower)},
		"upper":   {1, 1, strFunc(strings.ToUpper)},
		"length":  {1, 1, fnLength},
		// 类型转换
		"int":    {1, 1, fnInt},
		"float":  {1, 1, fnFloat},
		"string": {1, 1, fnString},
		"bool":   {1, 1, fnBool},
		"date":   {1, 2, fnDate},
		// SQL 专用
		"join":      {2, 2, fnJoin},
		"like":      {1, 1, likeFunc(true, true)},
		"likeLeft":  {1, 1, likeFunc(true, false)},
		"likeRight": {1, 1, likeFunc(false, true)},
		"ident":     {2, 2, fnIdent},
		"raw":       {1, 1, fnRaw},
		// 判断
		"isDefined":  {1, 1, func(_ *execCtx, a []any) (any, error) { return !isUndefined(a[0]), nil }},
		"isNull":     {1, 1, func(_ *execCtx, a []any) (any, error) { return a[0] == nil, nil }},
		"isEmpty":    {1, 1, func(_ *execCtx, a []any) (any, error) { return isEmpty(a[0]), nil }},
		"notIsEmpty": {1, 1, func(_ *execCtx, a []any) (any, error) { return !isEmpty(a[0]), nil }},
		// 多语言
		"t": {1, 1, fnT},
	}
}

// absent 表示 undefined 或 null。多数函数对这两种值原样透传，
// 这样 $.x|trim|default("") 在字段缺失时也能工作。
func absent(v any) bool {
	return v == nil || isUndefined(v)
}

func fnDefault(_ *execCtx, a []any) (any, error) {
	if isEmpty(a[0]) {
		return a[1], nil
	}
	return a[0], nil
}

// strFunc 只转换字符串，其他类型原样返回。
func strFunc(f func(string) string) func(*execCtx, []any) (any, error) {
	return func(_ *execCtx, a []any) (any, error) {
		if s, ok := a[0].(string); ok {
			return f(s), nil
		}
		return a[0], nil
	}
}

func fnLength(_ *execCtx, a []any) (any, error) {
	switch x := a[0].(type) {
	case undefinedType, nil:
		return int64(0), nil
	case string:
		return int64(runeLen(x)), nil
	case []any:
		return int64(len(x)), nil
	case map[string]any:
		return int64(len(x)), nil
	}
	return nil, fmt.Errorf("expects a string or an array, got %s", typeName(a[0]))
}

func fnInt(_ *execCtx, a []any) (any, error) {
	switch x := a[0].(type) {
	case undefinedType, nil:
		return x, nil
	case int64:
		return x, nil
	case float64:
		return int64(math.Trunc(x)), nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, ok := looseFloat(s); ok {
			return int64(math.Trunc(f)), nil
		}
		return nil, fmt.Errorf("cannot convert %q to int", x)
	}
	return nil, fmt.Errorf("cannot convert %s to int", typeName(a[0]))
}

func fnFloat(_ *execCtx, a []any) (any, error) {
	switch x := a[0].(type) {
	case undefinedType, nil:
		return x, nil
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	case string:
		if f, ok := looseFloat(x); ok {
			return f, nil
		}
		return nil, fmt.Errorf("cannot convert %q to float", x)
	}
	return nil, fmt.Errorf("cannot convert %s to float", typeName(a[0]))
}

func fnString(_ *execCtx, a []any) (any, error) {
	if absent(a[0]) {
		return a[0], nil
	}
	if s, ok := toStr(a[0]); ok {
		return s, nil
	}
	return nil, fmt.Errorf("cannot convert %s to string", typeName(a[0]))
}

func fnBool(_ *execCtx, a []any) (any, error) {
	switch x := a[0].(type) {
	case undefinedType, nil:
		return x, nil
	case bool:
		return x, nil
	case int64:
		if x == 0 || x == 1 {
			return x == 1, nil
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true":
			return true, nil
		case "0", "false":
			return false, nil
		}
		return nil, fmt.Errorf("cannot convert %q to bool", x)
	}
	return nil, fmt.Errorf("cannot convert %s to bool", typeName(a[0]))
}

// phpLayout 把 Twig / PHP 的日期格式（Y-m-d H:i:s）转成 Go 的 layout。
func phpLayout(f string) string {
	var sb strings.Builder
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case 'Y':
			sb.WriteString("2006")
		case 'y':
			sb.WriteString("06")
		case 'm':
			sb.WriteString("01")
		case 'n':
			sb.WriteString("1")
		case 'd':
			sb.WriteString("02")
		case 'j':
			sb.WriteString("2")
		case 'H':
			sb.WriteString("15")
		case 'i':
			sb.WriteString("04")
		case 's':
			sb.WriteString("05")
		case '\\':
			if i+1 < len(f) {
				i++
				sb.WriteByte(f[i])
			}
		default:
			sb.WriteByte(f[i])
		}
	}
	return sb.String()
}

func fnDate(_ *execCtx, a []any) (any, error) {
	if absent(a[0]) {
		return a[0], nil
	}
	format := "Y-m-d H:i:s"
	if len(a) == 2 {
		s, ok := a[1].(string)
		if !ok {
			return nil, fmt.Errorf("format must be a string")
		}
		format = s
	}
	var t time.Time
	switch x := a[0].(type) {
	case int64:
		t = time.Unix(x, 0)
	default:
		var ok bool
		if t, ok = parseDate(x); !ok {
			return nil, fmt.Errorf("cannot parse %v as a date", x)
		}
	}
	return t.Format(phpLayout(format)), nil
}

func fnJoin(_ *execCtx, a []any) (any, error) {
	items, ok := a[0].([]any)
	if !ok {
		return nil, fmt.Errorf("expects an array, got %s", typeName(a[0]))
	}
	sep, _ := a[1].(string)
	return joined{items: items, sep: sep}, nil
}

// likeFunc 转义 LIKE 的通配符并在两侧加 %。转义符固定为 !，
// 模板里需要配套写 escape '!'。
func likeFunc(left, right bool) func(*execCtx, []any) (any, error) {
	return func(c *execCtx, a []any) (any, error) {
		if absent(a[0]) {
			return a[0], nil
		}
		s, ok := toStr(a[0])
		if !ok {
			return nil, fmt.Errorf("expects a string, got %s", typeName(a[0]))
		}
		var sb strings.Builder
		if left {
			sb.WriteByte('%')
		}
		for i := 0; i < len(s); i++ {
			ch := s[i]
			// SQL Server 的 LIKE 还把 [ 当作通配符
			if ch == '!' || ch == '%' || ch == '_' || (ch == '[' && c.eng.dialect == SQLServer) {
				sb.WriteByte('!')
			}
			sb.WriteByte(ch)
		}
		if right {
			sb.WriteByte('%')
		}
		return sb.String(), nil
	}
}

func fnIdent(_ *execCtx, a []any) (any, error) {
	s, ok := a[0].(string)
	if !ok {
		return nil, fmt.Errorf("expects a string, got %s", typeName(a[0]))
	}
	list, _ := a[1].(string)
	for _, allowed := range strings.Split(list, ",") {
		if allowed = strings.TrimSpace(allowed); allowed != "" && allowed == s {
			return rawSQL(s), nil
		}
	}
	return nil, fmt.Errorf("%q is not in the whitelist (%s)", s, list)
}

func fnRaw(_ *execCtx, a []any) (any, error) {
	s, ok := toStr(a[0])
	if !ok {
		return nil, fmt.Errorf("expects a string, got %s", typeName(a[0]))
	}
	return rawSQL(s), nil
}

// fnT 按当前语言取文案：先查 $.lang.data.<当前语言>.<key>，
// 找不到回退到默认语言（$.lang.default，未设置时为 en），仍找不到返回 key 本身。
func fnT(c *execCtx, a []any) (any, error) {
	key, ok := a[0].(string)
	if !ok {
		return nil, fmt.Errorf("expects a string key, got %s", typeName(a[0]))
	}
	lang, _ := c.root["lang"].(map[string]any)
	data, _ := lang["data"].(map[string]any)
	lookup := func(name any) (string, bool) {
		n, ok := name.(string)
		if !ok || n == "" {
			return "", false
		}
		m, _ := data[n].(map[string]any)
		v, ok := m[key]
		if !ok {
			return "", false
		}
		return toStr(v)
	}
	if s, ok := lookup(lang["current"]); ok {
		return s, nil
	}
	fallback := lang["default"]
	if fallback == nil {
		fallback = "en"
	}
	if s, ok := lookup(fallback); ok {
		return s, nil
	}
	return key, nil
}

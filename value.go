package drusql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type undefinedType struct{}

func (undefinedType) String() string { return "undefined" }

// Undefined 表示路径不存在时的求值结果。自定义函数可以用它判断参数是否缺失。
var Undefined = undefinedType{}

// rawSQL 是直接拼入 SQL 文本的片段，由 ident 和 raw 产生。
type rawSQL string

// joined 是 join 的结果：每个元素单独绑定，占位符之间用 sep 分隔。
type joined struct {
	items []any
	sep   string
}

func isUndefined(v any) bool {
	_, ok := v.(undefinedType)
	return ok
}

// isEmpty 对应模板里的 isEmpty：未定义、null、空字符串、空数组。
func isEmpty(v any) bool {
	switch x := v.(type) {
	case undefinedType:
		return true
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	}
	return false
}

const maxSafeInt = 1 << 53

// normalize 把调用方传入的任意 Go 值转成引擎内部使用的形式：
// 整数 -> int64，小数 -> float64，数组 -> []any，对象 -> map[string]any。
// 结果是深拷贝，不会修改调用方的数据。
func normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case undefinedType:
		return x
	case string:
		return x
	case bool:
		return x
	case int64:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < maxSafeInt {
			return int64(x)
		}
		return x
	case int:
		return int64(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return normalize(f)
		}
		return x.String()
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalize(e)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = normalize(e)
		}
		return a
	case time.Time:
		return x
	case []byte:
		return x
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return float64(u)
		}
		return int64(u)
	case reflect.Float32, reflect.Float64:
		return normalize(rv.Float())
	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return rv.Bool()
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		a := make([]any, rv.Len())
		for i := range a {
			a[i] = normalize(rv.Index(i).Interface())
		}
		return a
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		m := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			m[iter.Key().String()] = normalize(iter.Value().Interface())
		}
		return m
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return normalize(rv.Elem().Interface())
	case reflect.Struct:
		// 结构体按它的 JSON 形式参与路径访问（字段名取 json 标签）
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var out any
		if err := dec.Decode(&out); err != nil {
			return v
		}
		return normalize(out)
	}
	return v
}

func typeName(v any) string {
	switch v.(type) {
	case undefinedType:
		return "undefined"
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case int64, float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case rawSQL:
		return "raw sql"
	case joined:
		return "joined list"
	}
	return fmt.Sprintf("%T", v)
}

// toFloat 只接受真正的数字。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// looseFloat 额外接受数字形式的字符串，用于比较和校验。
func looseFloat(v any) (float64, bool) {
	if f, ok := toFloat(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return f, true
		}
	}
	return 0, false
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toStr 把标量转成字符串。
func toStr(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return formatFloat(x), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// valuesEqual 实现 == ：数字按数值比较，数字与数字形式的字符串按数值比较，
// 其余要求类型相同。
func valuesEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	_, an := toFloat(a)
	_, bn := toFloat(b)
	if an || bn {
		af, ok1 := looseFloat(a)
		bf, ok2 := looseFloat(b)
		return ok1 && ok2 && af == bf
	}
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return reflect.DeepEqual(a, b)
}

// valuesCompare 实现 > >= < <= 。无法比较时 ok 为 false。
func valuesCompare(a, b any) (cmp int, ok bool) {
	_, an := toFloat(a)
	_, bn := toFloat(b)
	if an || bn {
		af, ok1 := looseFloat(a)
		bf, ok2 := looseFloat(b)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch {
		case af < bf:
			return -1, true
		case af > bf:
			return 1, true
		}
		return 0, true
	}
	as, ok1 := a.(string)
	bs, ok2 := b.(string)
	if ok1 && ok2 {
		return strings.Compare(as, bs), true
	}
	return 0, false
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"2006/01/02 15:04:05",
	"2006/01/02",
}

func parseDate(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case string:
		s := strings.TrimSpace(x)
		for _, l := range dateLayouts {
			if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

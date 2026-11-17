package drusql

import (
	"fmt"
	"strings"
)

// ParseError 是模板加载阶段的错误：语法错误、未知函数、非法写法等。
type ParseError struct {
	Line int
	Col  int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("drusql: parse error at line %d, col %d: %s", e.Line, e.Col, e.Msg)
}

// ExecError 是执行阶段与请求数据有关的错误：undefined 落到值占位符、
// ident 不在白名单内、类型转换失败等。
type ExecError struct {
	Line int
	Col  int
	Msg  string
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("drusql: exec error at line %d, col %d: %s", e.Line, e.Col, e.Msg)
}

// ValidationError 是一条校验失败记录。
type ValidationError struct {
	// Field 是出错值的完整 JSONPath，[*] 已替换为具体下标。
	Field string `json:"field"`
	// Rule 是规则链中第一条没通过的规则名。
	Rule string `json:"rule"`
	// Message 是该条 validate 的 message 求值结果。
	Message string `json:"message"`
}

// ValidationErrors 是一次执行中收集到的全部校验失败项，按模板书写顺序排列。
type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = e.Field + ": " + e.Message
	}
	return "drusql: validation failed: " + strings.Join(parts, "; ")
}

// lineCol 把字节偏移换算成行列号（从 1 开始）。
func lineCol(src string, pos int) (int, int) {
	if pos > len(src) {
		pos = len(src)
	}
	line, col := 1, 1
	for i := 0; i < pos; i++ {
		if src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

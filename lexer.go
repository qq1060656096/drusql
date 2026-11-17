package drusql

import (
	"strconv"
	"strings"
)

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tNumber
	tPath
	tOp
)

type token struct {
	kind tokKind
	text string // 标识符名、运算符、路径原文
	str  string // 字符串字面量解码后的值
	num  any    // int64 或 float64
	pos  int    // 在模板源码中的字节偏移
}

type lexError struct {
	pos int
	msg string
}

func (e *lexError) Error() string { return e.msg }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

var twoCharOps = []string{"==", "!=", ">=", "<=", "&&", "||"}

const oneCharOps = "><!+-*/%~|()[],:="

// lexTag 从 start（紧跟在 "{{" 之后）开始切词，直到遇到不在字符串和花括号内的 "}}"。
// 返回词法单元和 "}}" 之后的位置。
func lexTag(src string, start int) ([]token, int, error) {
	var toks []token
	depth := 0
	i := start
	for {
		for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
			i++
		}
		if i >= len(src) {
			return nil, 0, &lexError{start - 2, "unclosed tag: missing }}"}
		}
		c := src[i]
		switch {
		case c == '}':
			if depth > 0 {
				depth--
				toks = append(toks, token{kind: tOp, text: "}", pos: i})
				i++
				continue
			}
			if i+1 < len(src) && src[i+1] == '}' {
				return toks, i + 2, nil
			}
			return nil, 0, &lexError{i, "unexpected }"}
		case c == '{':
			depth++
			toks = append(toks, token{kind: tOp, text: "{", pos: i})
			i++
		case c == '"' || c == '\'':
			s, end, err := lexString(src, i)
			if err != nil {
				return nil, 0, err
			}
			toks = append(toks, token{kind: tString, str: s, text: src[i:end], pos: i})
			i = end
		case isDigit(c):
			j := i
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			isFloat := false
			if j+1 < len(src) && src[j] == '.' && isDigit(src[j+1]) {
				isFloat = true
				j++
				for j < len(src) && isDigit(src[j]) {
					j++
				}
			}
			text := src[i:j]
			var num any
			if isFloat {
				f, err := strconv.ParseFloat(text, 64)
				if err != nil {
					return nil, 0, &lexError{i, "invalid number " + text}
				}
				num = f
			} else {
				n, err := strconv.ParseInt(text, 10, 64)
				if err != nil {
					return nil, 0, &lexError{i, "invalid number " + text}
				}
				num = n
			}
			toks = append(toks, token{kind: tNumber, text: text, num: num, pos: i})
			i = j
		case c == '$':
			end, err := scanPath(src, i)
			if err != nil {
				return nil, 0, err
			}
			toks = append(toks, token{kind: tPath, text: src[i:end], pos: i})
			i = end
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			toks = append(toks, token{kind: tIdent, text: src[i:j], pos: i})
			i = j
		default:
			if i+1 < len(src) {
				two := src[i : i+2]
				matched := false
				for _, op := range twoCharOps {
					if two == op {
						toks = append(toks, token{kind: tOp, text: op, pos: i})
						i += 2
						matched = true
						break
					}
				}
				if matched {
					continue
				}
			}
			if strings.IndexByte(oneCharOps, c) >= 0 {
				toks = append(toks, token{kind: tOp, text: string(c), pos: i})
				i++
				continue
			}
			return nil, 0, &lexError{i, "unexpected character " + strconv.Quote(string(c))}
		}
	}
}

// lexString 解析从 i 开始的字符串字面量（单引号或双引号），返回值和结束位置。
func lexString(src string, i int) (string, int, error) {
	quote := src[i]
	var sb strings.Builder
	j := i + 1
	for j < len(src) {
		c := src[j]
		if c == quote {
			return sb.String(), j + 1, nil
		}
		if c == '\\' && j+1 < len(src) {
			j++
			switch src[j] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			default:
				sb.WriteByte(src[j])
			}
			j++
			continue
		}
		sb.WriteByte(c)
		j++
	}
	return "", 0, &lexError{i, "unclosed string literal"}
}

// scanPath 找出从 i（'$' 的位置）开始的 JSONPath 的结束位置。
// 这里只确定边界，具体语法由 parsePath 检查。
func scanPath(src string, i int) (int, error) {
	j := i + 1
	// 循环变量：$v
	for j < len(src) && isIdentChar(src[j]) {
		j++
	}
	for j < len(src) {
		switch src[j] {
		case '.':
			k := j + 1
			if k < len(src) && src[k] == '.' {
				k++
			}
			if k < len(src) && src[k] == '*' {
				j = k + 1
				continue
			}
			if k < len(src) && src[k] == '[' {
				j = k
				continue
			}
			if k < len(src) && isIdentStart(src[k]) {
				for k < len(src) && isIdentChar(src[k]) {
					k++
				}
				j = k
				continue
			}
			return 0, &lexError{j, "invalid path: expected a name after '.'"}
		case '[':
			end, err := matchBracket(src, j)
			if err != nil {
				return 0, err
			}
			j = end
		default:
			return j, nil
		}
	}
	return j, nil
}

// matchBracket 返回与 src[i] 处 '[' 配对的 ']' 之后的位置，跳过字符串和嵌套括号。
func matchBracket(src string, i int) (int, error) {
	depth := 0
	j := i
	for j < len(src) {
		switch src[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j + 1, nil
			}
		case '"', '\'':
			_, end, err := lexString(src, j)
			if err != nil {
				return 0, err
			}
			j = end
			continue
		}
		j++
	}
	return 0, &lexError{i, "invalid path: unclosed '['"}
}

package drusql

import (
	"fmt"
	"strconv"
	"strings"
)

// 本文件实现模板里用到的 JSONPath（RFC 9535）：成员、下标、通配、切片、
// 过滤和递归下降。过滤表达式不支持 RFC 中的函数扩展（length()、match() 等）。

type selKind int

const (
	selName selKind = iota
	selIndex
	selWildcard
	selSlice
	selFilter
)

type selector struct {
	kind   selKind
	name   string
	index  int
	slice  [3]*int // start, end, step
	filter filterExpr
}

type segment struct {
	descendant bool
	selectors  []selector
}

type pathExpr struct {
	src      string
	local    string // 循环变量名；空表示从根 $ 开始
	relative bool   // 过滤表达式里的 @
	segs     []segment
	singular bool // 只含成员和下标，求值结果是单个值
}

type pathNode struct {
	val  any
	path string
}

type pathParser struct {
	s string
	i int
}

func (p *pathParser) errf(format string, args ...any) error {
	return fmt.Errorf("invalid path %q: %s", p.s, fmt.Sprintf(format, args...))
}

func (p *pathParser) skipWS() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

// parsePath 解析一个完整的路径词法单元，如 $.request.body.users[0].id 或 $v.user_id。
func parsePath(s string) (*pathExpr, error) {
	p := &pathParser{s: s}
	pe, err := p.parseQuery()
	if err != nil {
		return nil, err
	}
	if p.i != len(s) {
		return nil, p.errf("unexpected %q", s[p.i:])
	}
	pe.src = s
	return pe, nil
}

// parseQuery 解析以 $ 或 @ 开头的查询，停在第一个不属于路径的字符上。
func (p *pathParser) parseQuery() (*pathExpr, error) {
	start := p.i
	pe := &pathExpr{}
	switch {
	case p.i < len(p.s) && p.s[p.i] == '@':
		pe.relative = true
		p.i++
	case p.i < len(p.s) && p.s[p.i] == '$':
		p.i++
		j := p.i
		for j < len(p.s) && isIdentChar(p.s[j]) {
			j++
		}
		pe.local = p.s[p.i:j]
		p.i = j
	default:
		return nil, p.errf("expected $ or @")
	}
	pe.singular = true
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '.' {
			seg := segment{}
			p.i++
			if p.i < len(p.s) && p.s[p.i] == '.' {
				seg.descendant = true
				p.i++
			}
			switch {
			case p.i < len(p.s) && p.s[p.i] == '*':
				p.i++
				seg.selectors = []selector{{kind: selWildcard}}
			case p.i < len(p.s) && p.s[p.i] == '[' && seg.descendant:
				sels, err := p.parseBracket()
				if err != nil {
					return nil, err
				}
				seg.selectors = sels
			case p.i < len(p.s) && isIdentStart(p.s[p.i]):
				j := p.i
				for j < len(p.s) && isIdentChar(p.s[j]) {
					j++
				}
				seg.selectors = []selector{{kind: selName, name: p.s[p.i:j]}}
				p.i = j
			default:
				return nil, p.errf("expected a name after '.'")
			}
			pe.segs = append(pe.segs, seg)
		} else if c == '[' {
			sels, err := p.parseBracket()
			if err != nil {
				return nil, err
			}
			pe.segs = append(pe.segs, segment{selectors: sels})
		} else {
			break
		}
	}
	for _, seg := range pe.segs {
		if seg.descendant || len(seg.selectors) != 1 {
			pe.singular = false
			break
		}
		if k := seg.selectors[0].kind; k != selName && k != selIndex {
			pe.singular = false
			break
		}
	}
	pe.src = p.s[start:p.i]
	return pe, nil
}

func (p *pathParser) parseBracket() ([]selector, error) {
	p.i++ // '['
	var sels []selector
	for {
		p.skipWS()
		if p.i >= len(p.s) {
			return nil, p.errf("unclosed '['")
		}
		sel, err := p.parseSelector()
		if err != nil {
			return nil, err
		}
		sels = append(sels, sel)
		p.skipWS()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return sels, nil
		}
		return nil, p.errf("expected ',' or ']'")
	}
}

func (p *pathParser) parseSelector() (selector, error) {
	c := p.s[p.i]
	switch {
	case c == '*':
		p.i++
		return selector{kind: selWildcard}, nil
	case c == '\'' || c == '"':
		s, end, err := lexString(p.s, p.i)
		if err != nil {
			return selector{}, p.errf("unclosed string")
		}
		p.i = end
		return selector{kind: selName, name: s}, nil
	case c == '?':
		p.i++
		f, err := p.parseFilterOr()
		if err != nil {
			return selector{}, err
		}
		return selector{kind: selFilter, filter: f}, nil
	}
	// 下标或切片
	first, hasFirst, err := p.parseInt()
	if err != nil {
		return selector{}, err
	}
	p.skipWS()
	if p.i < len(p.s) && p.s[p.i] == ':' {
		sel := selector{kind: selSlice}
		if hasFirst {
			v := first
			sel.slice[0] = &v
		}
		p.i++
		p.skipWS()
		end, hasEnd, err := p.parseInt()
		if err != nil {
			return selector{}, err
		}
		if hasEnd {
			v := end
			sel.slice[1] = &v
		}
		p.skipWS()
		if p.i < len(p.s) && p.s[p.i] == ':' {
			p.i++
			p.skipWS()
			step, hasStep, err := p.parseInt()
			if err != nil {
				return selector{}, err
			}
			if hasStep {
				v := step
				sel.slice[2] = &v
			}
		}
		return sel, nil
	}
	if !hasFirst {
		return selector{}, p.errf("expected a selector at %q", p.s[p.i:])
	}
	return selector{kind: selIndex, index: first}, nil
}

func (p *pathParser) parseInt() (int, bool, error) {
	j := p.i
	if j < len(p.s) && p.s[j] == '-' {
		j++
	}
	k := j
	for k < len(p.s) && isDigit(p.s[k]) {
		k++
	}
	if k == j {
		return 0, false, nil
	}
	n, err := strconv.Atoi(p.s[p.i:k])
	if err != nil {
		return 0, false, p.errf("invalid index %q", p.s[p.i:k])
	}
	p.i = k
	return n, true, nil
}

// ---- 过滤表达式 ----

type filterExpr interface {
	test(cur, root any) bool
}

type filterOr struct{ l, r filterExpr }
type filterAnd struct{ l, r filterExpr }
type filterNot struct{ x filterExpr }
type filterExists struct{ q *pathExpr }
type filterCmp struct {
	op   string
	l, r filterOperand
}

type filterOperand struct {
	isLit bool
	lit   any
	q     *pathExpr
}

func (f filterOr) test(cur, root any) bool  { return f.l.test(cur, root) || f.r.test(cur, root) }
func (f filterAnd) test(cur, root any) bool { return f.l.test(cur, root) && f.r.test(cur, root) }
func (f filterNot) test(cur, root any) bool { return !f.x.test(cur, root) }
func (f filterExists) test(cur, root any) bool {
	return len(f.q.nodes(root, cur)) > 0
}

func (o filterOperand) value(cur, root any) (any, bool) {
	if o.isLit {
		return o.lit, true
	}
	ns := o.q.nodes(root, cur)
	if len(ns) != 1 {
		return nil, false
	}
	return ns[0].val, true
}

func (f filterCmp) test(cur, root any) bool {
	a, aok := f.l.value(cur, root)
	b, bok := f.r.value(cur, root)
	eq := func() bool {
		if !aok || !bok {
			return !aok && !bok
		}
		return valuesEqual(a, b)
	}
	lt := func(x, y any, xok, yok bool) bool {
		if !xok || !yok {
			return false
		}
		c, ok := valuesCompare(x, y)
		return ok && c < 0
	}
	switch f.op {
	case "==":
		return eq()
	case "!=":
		return !eq()
	case "<":
		return lt(a, b, aok, bok)
	case ">":
		return lt(b, a, bok, aok)
	case "<=":
		return lt(a, b, aok, bok) || eq()
	case ">=":
		return lt(b, a, bok, aok) || eq()
	}
	return false
}

func (p *pathParser) parseFilterOr() (filterExpr, error) {
	l, err := p.parseFilterAnd()
	if err != nil {
		return nil, err
	}
	for {
		p.skipWS()
		if strings.HasPrefix(p.s[p.i:], "||") {
			p.i += 2
			r, err := p.parseFilterAnd()
			if err != nil {
				return nil, err
			}
			l = filterOr{l, r}
			continue
		}
		return l, nil
	}
}

func (p *pathParser) parseFilterAnd() (filterExpr, error) {
	l, err := p.parseFilterBasic()
	if err != nil {
		return nil, err
	}
	for {
		p.skipWS()
		if strings.HasPrefix(p.s[p.i:], "&&") {
			p.i += 2
			r, err := p.parseFilterBasic()
			if err != nil {
				return nil, err
			}
			l = filterAnd{l, r}
			continue
		}
		return l, nil
	}
}

func (p *pathParser) parseFilterBasic() (filterExpr, error) {
	p.skipWS()
	if p.i >= len(p.s) {
		return nil, p.errf("incomplete filter")
	}
	if p.s[p.i] == '!' && !strings.HasPrefix(p.s[p.i:], "!=") {
		p.i++
		x, err := p.parseFilterBasic()
		if err != nil {
			return nil, err
		}
		return filterNot{x}, nil
	}
	if p.s[p.i] == '(' {
		p.i++
		x, err := p.parseFilterOr()
		if err != nil {
			return nil, err
		}
		p.skipWS()
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return nil, p.errf("expected ')' in filter")
		}
		p.i++
		return x, nil
	}
	l, err := p.parseFilterOperand()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if strings.HasPrefix(p.s[p.i:], op) {
			p.i += len(op)
			p.skipWS()
			r, err := p.parseFilterOperand()
			if err != nil {
				return nil, err
			}
			for _, o := range []filterOperand{l, r} {
				if !o.isLit && !o.q.singular {
					return nil, p.errf("only singular queries can be compared in a filter: %s", o.q.src)
				}
			}
			return filterCmp{op: op, l: l, r: r}, nil
		}
	}
	if l.isLit {
		return nil, p.errf("a literal alone is not a valid filter")
	}
	return filterExists{l.q}, nil
}

func (p *pathParser) parseFilterOperand() (filterOperand, error) {
	p.skipWS()
	if p.i >= len(p.s) {
		return filterOperand{}, p.errf("incomplete filter")
	}
	c := p.s[p.i]
	switch {
	case c == '@' || c == '$':
		q, err := p.parseQuery()
		if err != nil {
			return filterOperand{}, err
		}
		if q.local != "" {
			return filterOperand{}, p.errf("loop variables cannot be used inside a filter")
		}
		return filterOperand{q: q}, nil
	case c == '\'' || c == '"':
		s, end, err := lexString(p.s, p.i)
		if err != nil {
			return filterOperand{}, p.errf("unclosed string")
		}
		p.i = end
		return filterOperand{isLit: true, lit: s}, nil
	case c == '-' || isDigit(c):
		j := p.i
		if p.s[j] == '-' {
			j++
		}
		for j < len(p.s) && (isDigit(p.s[j]) || p.s[j] == '.') {
			j++
		}
		text := p.s[p.i:j]
		p.i = j
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return filterOperand{isLit: true, lit: n}, nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return filterOperand{}, p.errf("invalid number %q", text)
		}
		return filterOperand{isLit: true, lit: f}, nil
	}
	for word, val := range map[string]any{"true": true, "false": false, "null": nil} {
		if strings.HasPrefix(p.s[p.i:], word) {
			end := p.i + len(word)
			if end >= len(p.s) || !isIdentChar(p.s[end]) {
				p.i = end
				return filterOperand{isLit: true, lit: val}, nil
			}
		}
	}
	return filterOperand{}, p.errf("unsupported filter expression at %q", p.s[p.i:])
}

// ---- 求值 ----

func childPath(parent, name string) string {
	simple := name != "" && isIdentStart(name[0])
	for i := 0; simple && i < len(name); i++ {
		if !isIdentChar(name[i]) {
			simple = false
		}
	}
	if simple {
		return parent + "." + name
	}
	return parent + "['" + strings.ReplaceAll(name, "'", "\\'") + "']"
}

func indexPath(parent string, i int) string {
	return parent + "[" + strconv.Itoa(i) + "]"
}

// nodes 求出路径匹配的全部节点。root 是 $，cur 是 @（或循环变量的值）。
func (pe *pathExpr) nodes(root, cur any) []pathNode {
	var start pathNode
	switch {
	case pe.relative:
		start = pathNode{cur, "@"}
	case pe.local != "":
		start = pathNode{cur, "$" + pe.local}
	default:
		start = pathNode{root, "$"}
	}
	if isUndefined(start.val) {
		return nil
	}
	list := []pathNode{start}
	for _, seg := range pe.segs {
		var next []pathNode
		for _, n := range list {
			if seg.descendant {
				walkDescendants(n, func(d pathNode) {
					for _, sel := range seg.selectors {
						next = appendSelected(next, d, sel, root)
					}
				})
			} else {
				for _, sel := range seg.selectors {
					next = appendSelected(next, n, sel, root)
				}
			}
		}
		list = next
		if len(list) == 0 {
			return nil
		}
	}
	return list
}

func walkDescendants(n pathNode, visit func(pathNode)) {
	visit(n)
	switch v := n.val.(type) {
	case []any:
		for i, e := range v {
			walkDescendants(pathNode{e, indexPath(n.path, i)}, visit)
		}
	case map[string]any:
		for _, k := range sortedKeys(v) {
			walkDescendants(pathNode{v[k], childPath(n.path, k)}, visit)
		}
	}
}

func appendSelected(out []pathNode, n pathNode, sel selector, root any) []pathNode {
	switch sel.kind {
	case selName:
		if m, ok := n.val.(map[string]any); ok {
			if v, ok := m[sel.name]; ok {
				out = append(out, pathNode{v, childPath(n.path, sel.name)})
			}
		}
	case selIndex:
		if a, ok := n.val.([]any); ok {
			i := sel.index
			if i < 0 {
				i += len(a)
			}
			if i >= 0 && i < len(a) {
				out = append(out, pathNode{a[i], indexPath(n.path, i)})
			}
		}
	case selWildcard:
		switch v := n.val.(type) {
		case []any:
			for i, e := range v {
				out = append(out, pathNode{e, indexPath(n.path, i)})
			}
		case map[string]any:
			for _, k := range sortedKeys(v) {
				out = append(out, pathNode{v[k], childPath(n.path, k)})
			}
		}
	case selSlice:
		if a, ok := n.val.([]any); ok {
			for _, i := range sliceIndexes(len(a), sel.slice) {
				out = append(out, pathNode{a[i], indexPath(n.path, i)})
			}
		}
	case selFilter:
		switch v := n.val.(type) {
		case []any:
			for i, e := range v {
				if sel.filter.test(e, root) {
					out = append(out, pathNode{e, indexPath(n.path, i)})
				}
			}
		case map[string]any:
			for _, k := range sortedKeys(v) {
				if sel.filter.test(v[k], root) {
					out = append(out, pathNode{v[k], childPath(n.path, k)})
				}
			}
		}
	}
	return out
}

// sliceIndexes 按 RFC 9535 的规则计算切片选中的下标。
func sliceIndexes(n int, s [3]*int) []int {
	step := 1
	if s[2] != nil {
		step = *s[2]
	}
	if step == 0 {
		return nil
	}
	norm := func(i int) int {
		if i >= 0 {
			return i
		}
		return n + i
	}
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	var out []int
	if step > 0 {
		start, end := 0, n
		if s[0] != nil {
			start = *s[0]
		}
		if s[1] != nil {
			end = *s[1]
		}
		lower := clamp(norm(start), 0, n)
		upper := clamp(norm(end), 0, n)
		for i := lower; i < upper; i += step {
			out = append(out, i)
		}
		return out
	}
	start, end := n-1, -n-1
	if s[0] != nil {
		start = *s[0]
	}
	if s[1] != nil {
		end = *s[1]
	}
	upper := clamp(norm(start), -1, n-1)
	lower := clamp(norm(end), -1, n-1)
	for i := upper; i > lower; i += step {
		out = append(out, i)
	}
	return out
}

// value 返回路径在表达式里的求值结果：
// 只含成员和下标的路径返回单个值（不存在时为 Undefined），其余始终返回数组。
func (pe *pathExpr) value(root, cur any) any {
	ns := pe.nodes(root, cur)
	if pe.singular {
		if len(ns) == 0 {
			return Undefined
		}
		return ns[0].val
	}
	out := make([]any, len(ns))
	for i, n := range ns {
		out[i] = n.val
	}
	return out
}

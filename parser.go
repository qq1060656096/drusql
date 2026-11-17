package drusql

import (
	"fmt"
	"strings"
)

// ---- 表达式 AST ----

type expr interface{ exprPos() int }

type litExpr struct {
	val any
	pos int
}

type pathRef struct {
	p   *pathExpr
	pos int
}

type unaryExpr struct {
	op  string // "not" 或 "-"
	x   expr
	pos int
}

type binaryExpr struct {
	op   string
	l, r expr
	pos  int
}

type callExpr struct {
	name string
	args []expr
	pos  int
}

type arrayExpr struct {
	items []expr
	pos   int
}

type objectExpr struct {
	keys []string
	vals []expr
	pos  int
}

func (e *litExpr) exprPos() int    { return e.pos }
func (e *pathRef) exprPos() int    { return e.pos }
func (e *unaryExpr) exprPos() int  { return e.pos }
func (e *binaryExpr) exprPos() int { return e.pos }
func (e *callExpr) exprPos() int   { return e.pos }
func (e *arrayExpr) exprPos() int  { return e.pos }
func (e *objectExpr) exprPos() int { return e.pos }

// ---- 模板 AST ----

type node interface{}

type textNode struct{ text string }

type valueNode struct {
	x   expr
	pos int
}

type ifBranch struct {
	cond expr
	body []node
}

type ifNode struct {
	branches []ifBranch
	elseBody []node
}

type forNode struct {
	varName string
	x       expr
	sep     string
	hasSep  bool
	body    []node
	hasSet  bool
	pos     int
}

type setNode struct {
	path []string // $ 之后的成员名
	src  string
	x    expr
	pos  int
}

type validateNode struct {
	path  *pathExpr
	rules []rule
	msg   expr
	pos   int
}

var reserved = map[string]bool{
	"if": true, "elseif": true, "else": true, "for": true, "in": true, "sep": true,
	"validate": true, "set": true, "and": true, "or": true, "not": true,
	"true": true, "false": true, "null": true,
}

// ---- 模板切分 ----

type segKind int

const (
	segText segKind = iota
	segTag
	segComment
)

type rawSeg struct {
	kind segKind
	text string
	toks []token
	pos  int
	end  int
}

type tplParser struct {
	eng    *Engine
	src    string
	segs   []rawSeg
	i      int
	locals []string
}

func (p *tplParser) errAt(pos int, format string, args ...any) error {
	line, col := lineCol(p.src, pos)
	return &ParseError{Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}
}

func (p *tplParser) scan() error {
	src := p.src
	i := 0
	for i < len(src) {
		j := strings.IndexByte(src[i:], '{')
		if j < 0 {
			break
		}
		j += i
		if j+1 >= len(src) || (src[j+1] != '{' && src[j+1] != '#') {
			// 普通的 '{'，留在文本里
			p.appendText(src[i:j+1], i)
			i = j + 1
			continue
		}
		p.appendText(src[i:j], i)
		if src[j+1] == '#' {
			end := strings.Index(src[j+2:], "#}")
			if end < 0 {
				return p.errAt(j, "unclosed comment: missing #}")
			}
			i = j + 2 + end + 2
			p.segs = append(p.segs, rawSeg{kind: segComment, pos: j, end: i})
			continue
		}
		toks, end, err := lexTag(src, j+2)
		if err != nil {
			if le, ok := err.(*lexError); ok {
				return p.errAt(le.pos, "%s", le.msg)
			}
			return p.errAt(j, "%s", err.Error())
		}
		if len(toks) == 0 {
			return p.errAt(j, "empty tag")
		}
		p.segs = append(p.segs, rawSeg{kind: segTag, toks: toks, pos: j, end: end})
		i = end
	}
	p.appendText(src[i:], i)
	p.trimStandalone()
	return nil
}

func (p *tplParser) appendText(s string, pos int) {
	if s == "" {
		return
	}
	if n := len(p.segs); n > 0 && p.segs[n-1].kind == segText {
		p.segs[n-1].text += s
		return
	}
	p.segs = append(p.segs, rawSeg{kind: segText, text: s, pos: pos})
}

func isControlTag(s rawSeg) bool {
	if s.kind == segComment {
		return true
	}
	if s.kind != segTag {
		return false
	}
	t := s.toks[0]
	if t.kind == tOp && t.text == "/" {
		return true
	}
	if t.kind != tIdent {
		return false
	}
	switch t.text {
	case "if", "elseif", "else", "for", "set", "validate":
		return true
	}
	return false
}

// trimStandalone 去掉“独占一行的控制标签”所在的整行（缩进和行尾换行），
// 这样 if / for / set / validate 不会在生成的 SQL 里留下空行。
// 值占位符和与其他内容同行的标签不受影响。
func (p *tplParser) trimStandalone() {
	n := len(p.segs)
	headCut := make([]int, n) // 文本段开头要去掉的字节数
	tailCut := make([]int, n) // 文本段保留到的位置
	for i := range p.segs {
		tailCut[i] = len(p.segs[i].text)
	}
	blank := func(s string) bool { return strings.Trim(s, " \t\r") == "" }
	for i, s := range p.segs {
		if !isControlTag(s) {
			continue
		}
		// 左侧
		leftOK := false
		leftIdx, leftKeep := -1, 0
		if i == 0 {
			leftOK = true
		} else if p.segs[i-1].kind == segText {
			t := p.segs[i-1].text
			if nl := strings.LastIndexByte(t, '\n'); nl >= 0 {
				if blank(t[nl+1:]) {
					leftOK, leftIdx, leftKeep = true, i-1, nl+1
				}
			} else if i-1 == 0 && blank(t) {
				leftOK, leftIdx, leftKeep = true, i-1, 0
			}
		}
		if !leftOK {
			continue
		}
		// 右侧
		rightOK := false
		rightIdx, rightCut := -1, 0
		if i == n-1 {
			rightOK = true
		} else if p.segs[i+1].kind == segText {
			t := p.segs[i+1].text
			if nl := strings.IndexByte(t, '\n'); nl >= 0 {
				if blank(t[:nl]) {
					rightOK, rightIdx, rightCut = true, i+1, nl+1
				}
			} else if i+1 == n-1 && blank(t) {
				rightOK, rightIdx, rightCut = true, i+1, len(t)
			}
		}
		if !rightOK {
			continue
		}
		if leftIdx >= 0 {
			tailCut[leftIdx] = leftKeep
		}
		if rightIdx >= 0 {
			headCut[rightIdx] = rightCut
		}
	}
	for i := range p.segs {
		if p.segs[i].kind != segText {
			continue
		}
		t := p.segs[i].text
		if headCut[i] >= tailCut[i] {
			p.segs[i].text = ""
		} else {
			p.segs[i].text = t[headCut[i]:tailCut[i]]
		}
	}
}

// ---- 模板结构解析 ----

// parseBlock 解析到某个结束标签或文件末尾为止。
// 返回结束标签的名字（"elseif"、"else"、"/if"、"/for" 或 ""）和它所在的段。
func (p *tplParser) parseBlock(depth int) ([]node, string, rawSeg, error) {
	var nodes []node
	for p.i < len(p.segs) {
		s := p.segs[p.i]
		p.i++
		switch s.kind {
		case segComment:
			continue
		case segText:
			if s.text != "" {
				nodes = append(nodes, &textNode{s.text})
			}
			continue
		}
		first := s.toks[0]
		if first.kind == tOp && first.text == "/" {
			if len(s.toks) != 2 || s.toks[1].kind != tIdent || (s.toks[1].text != "if" && s.toks[1].text != "for") {
				return nil, "", s, p.errAt(s.pos, "invalid closing tag; expected {{/if}} or {{/for}}")
			}
			return nodes, "/" + s.toks[1].text, s, nil
		}
		kw := ""
		if first.kind == tIdent {
			kw = first.text
		}
		switch kw {
		case "elseif", "else":
			return nodes, kw, s, nil
		case "if":
			n, err := p.parseIf(s, depth)
			if err != nil {
				return nil, "", s, err
			}
			nodes = append(nodes, n)
		case "for":
			n, err := p.parseFor(s, depth)
			if err != nil {
				return nil, "", s, err
			}
			nodes = append(nodes, n)
		case "set":
			n, err := p.parseSet(s)
			if err != nil {
				return nil, "", s, err
			}
			nodes = append(nodes, n)
		case "validate":
			if depth > 0 {
				return nil, "", s, p.errAt(s.pos, "validate must be at the top level, not inside if / for")
			}
			n, err := p.parseValidate(s)
			if err != nil {
				return nil, "", s, err
			}
			nodes = append(nodes, n)
		default:
			ep := &exprParser{p: p, toks: s.toks, end: s.end}
			x, err := ep.parseAll()
			if err != nil {
				return nil, "", s, err
			}
			if err := p.check(x, true); err != nil {
				return nil, "", s, err
			}
			nodes = append(nodes, &valueNode{x: x, pos: s.pos})
		}
	}
	return nodes, "", rawSeg{}, nil
}

func (p *tplParser) parseIf(s rawSeg, depth int) (node, error) {
	n := &ifNode{}
	cur := s
	for {
		ep := &exprParser{p: p, toks: cur.toks, i: 1, end: cur.end}
		cond, err := ep.parseAll()
		if err != nil {
			return nil, err
		}
		if err := p.check(cond, false); err != nil {
			return nil, err
		}
		body, end, endSeg, err := p.parseBlock(depth + 1)
		if err != nil {
			return nil, err
		}
		n.branches = append(n.branches, ifBranch{cond: cond, body: body})
		switch end {
		case "/if":
			return n, nil
		case "elseif":
			cur = endSeg
			continue
		case "else":
			if len(endSeg.toks) != 1 {
				return nil, p.errAt(endSeg.pos, "{{else}} takes no condition; use {{elseif ...}}")
			}
			body, end, endSeg2, err := p.parseBlock(depth + 1)
			if err != nil {
				return nil, err
			}
			if end != "/if" {
				return nil, p.unclosed(s, "if", end, endSeg2)
			}
			n.elseBody = body
			return n, nil
		default:
			return nil, p.unclosed(s, "if", end, endSeg)
		}
	}
}

func (p *tplParser) unclosed(open rawSeg, kind, got string, gotSeg rawSeg) error {
	if got == "" {
		return p.errAt(open.pos, "unclosed {{%s}}: missing {{/%s}}", kind, kind)
	}
	return p.errAt(gotSeg.pos, "unexpected {{%s}} inside {{%s}}", got, kind)
}

func (p *tplParser) parseFor(s rawSeg, depth int) (node, error) {
	toks := s.toks
	if len(toks) < 4 || toks[1].kind != tIdent || toks[2].kind != tIdent || toks[2].text != "in" {
		return nil, p.errAt(s.pos, `invalid for tag; expected {{for v in <expr> sep "<literal>"}}`)
	}
	name := toks[1].text
	if reserved[name] {
		return nil, p.errAt(toks[1].pos, "%q is a reserved word and cannot be a loop variable", name)
	}
	ep := &exprParser{p: p, toks: toks, i: 3, end: s.end}
	x, err := ep.parseOr()
	if err != nil {
		return nil, err
	}
	n := &forNode{varName: name, x: x, pos: s.pos}
	if ep.i < len(toks) {
		t := toks[ep.i]
		if t.kind != tIdent || t.text != "sep" {
			return nil, p.errAt(t.pos, "unexpected %q in for tag", t.text)
		}
		if ep.i+1 >= len(toks) || toks[ep.i+1].kind != tString {
			return nil, p.errAt(t.pos, "sep must be a string literal")
		}
		if ep.i+2 != len(toks) {
			return nil, p.errAt(toks[ep.i+2].pos, "unexpected %q after sep", toks[ep.i+2].text)
		}
		n.sep = toks[ep.i+1].str
		n.hasSep = true
	}
	if err := p.check(x, false); err != nil {
		return nil, err
	}
	p.locals = append(p.locals, name)
	body, end, endSeg, err := p.parseBlock(depth + 1)
	p.locals = p.locals[:len(p.locals)-1]
	if err != nil {
		return nil, err
	}
	if end != "/for" {
		return nil, p.unclosed(s, "for", end, endSeg)
	}
	n.body = body
	n.hasSet = containsSet(body)
	return n, nil
}

func containsSet(nodes []node) bool {
	for _, n := range nodes {
		switch x := n.(type) {
		case *setNode:
			return true
		case *ifNode:
			for _, b := range x.branches {
				if containsSet(b.body) {
					return true
				}
			}
			if containsSet(x.elseBody) {
				return true
			}
		case *forNode:
			if x.hasSet {
				return true
			}
		}
	}
	return false
}

func (p *tplParser) parseSet(s rawSeg) (node, error) {
	toks := s.toks
	if len(toks) < 4 || toks[1].kind != tPath || toks[2].kind != tOp || toks[2].text != "=" {
		return nil, p.errAt(s.pos, "invalid set tag; expected {{set $.path = <expr>}}")
	}
	pe, err := parsePath(toks[1].text)
	if err != nil {
		return nil, p.errAt(toks[1].pos, "%s", err.Error())
	}
	if pe.local != "" {
		return nil, p.errAt(toks[1].pos, "set can only assign to a path under $, not to a loop variable")
	}
	if len(pe.segs) == 0 {
		return nil, p.errAt(toks[1].pos, "set cannot replace the root $")
	}
	var names []string
	for _, seg := range pe.segs {
		if seg.descendant || len(seg.selectors) != 1 || seg.selectors[0].kind != selName {
			return nil, p.errAt(toks[1].pos, "set path supports member access only: %s", toks[1].text)
		}
		names = append(names, seg.selectors[0].name)
	}
	if names[0] == "request" {
		return nil, p.errAt(toks[1].pos, "$.request is read-only")
	}
	ep := &exprParser{p: p, toks: toks, i: 3, end: s.end}
	x, err := ep.parseAll()
	if err != nil {
		return nil, err
	}
	if err := p.check(x, false); err != nil {
		return nil, err
	}
	return &setNode{path: names, src: toks[1].text, x: x, pos: s.pos}, nil
}

func (p *tplParser) parseValidate(s rawSeg) (node, error) {
	toks := s.toks
	if len(toks) < 2 || toks[1].kind != tPath {
		return nil, p.errAt(s.pos, `invalid validate tag; expected {{validate $.path rule="..." message=<expr>}}`)
	}
	pe, err := parsePath(toks[1].text)
	if err != nil {
		return nil, p.errAt(toks[1].pos, "%s", err.Error())
	}
	if pe.local != "" {
		return nil, p.errAt(toks[1].pos, "validate path must start from $")
	}
	for _, seg := range pe.segs {
		ok := !seg.descendant && len(seg.selectors) == 1
		if ok {
			switch seg.selectors[0].kind {
			case selName, selWildcard:
			case selIndex:
				ok = seg.selectors[0].index >= 0
			default:
				ok = false
			}
		}
		if !ok {
			return nil, p.errAt(toks[1].pos, "validate path supports members, indexes and [*] only: %s", toks[1].text)
		}
	}
	n := &validateNode{path: pe, pos: s.pos}
	hasRule := false
	ep := &exprParser{p: p, toks: toks, i: 2, end: s.end}
	for ep.i < len(toks) {
		name := toks[ep.i]
		if name.kind != tIdent || ep.i+1 >= len(toks) || toks[ep.i+1].kind != tOp || toks[ep.i+1].text != "=" {
			return nil, p.errAt(name.pos, "invalid validate attribute; expected rule=... or message=...")
		}
		ep.i += 2
		switch name.text {
		case "rule":
			if ep.i >= len(toks) || toks[ep.i].kind != tString {
				return nil, p.errAt(name.pos, "rule must be a string literal")
			}
			rules, err := parseRules(toks[ep.i].str)
			if err != nil {
				return nil, p.errAt(toks[ep.i].pos, "%s", err.Error())
			}
			n.rules = rules
			hasRule = true
			ep.i++
		case "message":
			x, err := ep.parseOr()
			if err != nil {
				return nil, err
			}
			if err := p.check(x, false); err != nil {
				return nil, err
			}
			n.msg = x
		default:
			return nil, p.errAt(name.pos, "unknown validate attribute %q", name.text)
		}
	}
	if !hasRule {
		return nil, p.errAt(s.pos, "validate requires a rule attribute")
	}
	return n, nil
}

// check 做加载期的静态检查：函数是否存在、参数个数、join / ident 的字面量要求、
// 循环变量是否已定义。isValue 表示 x 是值占位符的最外层表达式。
func (p *tplParser) check(x expr, isValue bool) error {
	switch e := x.(type) {
	case *litExpr:
		return nil
	case *pathRef:
		if e.p.local != "" {
			for _, l := range p.locals {
				if l == e.p.local {
					return nil
				}
			}
			return p.errAt(e.pos, "unknown variable $%s", e.p.local)
		}
		return nil
	case *unaryExpr:
		return p.check(e.x, false)
	case *binaryExpr:
		if err := p.check(e.l, false); err != nil {
			return err
		}
		return p.check(e.r, false)
	case *arrayExpr:
		for _, it := range e.items {
			if err := p.check(it, false); err != nil {
				return err
			}
		}
		return nil
	case *objectExpr:
		for _, it := range e.vals {
			if err := p.check(it, false); err != nil {
				return err
			}
		}
		return nil
	case *callExpr:
		def, ok := p.eng.funcs[e.name]
		if !ok {
			return p.errAt(e.pos, "unknown function %q", e.name)
		}
		if len(e.args) < def.min || (def.max >= 0 && len(e.args) > def.max) {
			return p.errAt(e.pos, "wrong number of arguments for %s", e.name)
		}
		switch e.name {
		case "join":
			if !isValue {
				return p.errAt(e.pos, "join must be the last function of a value placeholder")
			}
			if lit, ok := e.args[1].(*litExpr); !ok || !isString(lit.val) {
				return p.errAt(e.pos, "the separator of join must be a string literal")
			}
		case "ident":
			if lit, ok := e.args[1].(*litExpr); !ok || !isString(lit.val) {
				return p.errAt(e.pos, "the whitelist of ident must be a string literal")
			}
		}
		for _, a := range e.args {
			if err := p.check(a, false); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

// ---- 表达式解析 ----

type exprParser struct {
	p    *tplParser
	toks []token
	i    int
	end  int // 标签结束位置，用于报错
}

func (ep *exprParser) peek() *token {
	if ep.i < len(ep.toks) {
		return &ep.toks[ep.i]
	}
	return nil
}

func (ep *exprParser) isOp(text string) bool {
	t := ep.peek()
	return t != nil && t.kind == tOp && t.text == text
}

func (ep *exprParser) isWord(text string) bool {
	t := ep.peek()
	return t != nil && t.kind == tIdent && t.text == text
}

func (ep *exprParser) errHere(format string, args ...any) error {
	pos := ep.end
	if t := ep.peek(); t != nil {
		pos = t.pos
	}
	return ep.p.errAt(pos, format, args...)
}

// parseAll 解析一个表达式并要求它用完全部词法单元。
func (ep *exprParser) parseAll() (expr, error) {
	if ep.i >= len(ep.toks) {
		return nil, ep.errHere("expected an expression")
	}
	x, err := ep.parseOr()
	if err != nil {
		return nil, err
	}
	if t := ep.peek(); t != nil {
		return nil, ep.errHere("unexpected %q", t.text)
	}
	return x, nil
}

func (ep *exprParser) parseOr() (expr, error) {
	l, err := ep.parseAnd()
	if err != nil {
		return nil, err
	}
	for ep.isWord("or") || ep.isOp("||") {
		pos := ep.peek().pos
		ep.i++
		r, err := ep.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op: "or", l: l, r: r, pos: pos}
	}
	return l, nil
}

func (ep *exprParser) parseAnd() (expr, error) {
	l, err := ep.parseCmp()
	if err != nil {
		return nil, err
	}
	for ep.isWord("and") || ep.isOp("&&") {
		pos := ep.peek().pos
		ep.i++
		r, err := ep.parseCmp()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op: "and", l: l, r: r, pos: pos}
	}
	return l, nil
}

func (ep *exprParser) parseCmp() (expr, error) {
	l, err := ep.parseAdd()
	if err != nil {
		return nil, err
	}
	for {
		t := ep.peek()
		if t == nil || t.kind != tOp {
			return l, nil
		}
		switch t.text {
		case "==", "!=", ">", ">=", "<", "<=":
		default:
			return l, nil
		}
		ep.i++
		r, err := ep.parseAdd()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op: t.text, l: l, r: r, pos: t.pos}
	}
}

func (ep *exprParser) parseAdd() (expr, error) {
	l, err := ep.parseMul()
	if err != nil {
		return nil, err
	}
	for ep.isOp("+") || ep.isOp("-") || ep.isOp("~") {
		t := ep.peek()
		ep.i++
		r, err := ep.parseMul()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op: t.text, l: l, r: r, pos: t.pos}
	}
	return l, nil
}

func (ep *exprParser) parseMul() (expr, error) {
	l, err := ep.parseUnary()
	if err != nil {
		return nil, err
	}
	for ep.isOp("*") || ep.isOp("/") || ep.isOp("%") {
		t := ep.peek()
		ep.i++
		r, err := ep.parseUnary()
		if err != nil {
			return nil, err
		}
		l = &binaryExpr{op: t.text, l: l, r: r, pos: t.pos}
	}
	return l, nil
}

func (ep *exprParser) parseUnary() (expr, error) {
	if ep.isWord("not") || ep.isOp("!") {
		pos := ep.peek().pos
		ep.i++
		x, err := ep.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryExpr{op: "not", x: x, pos: pos}, nil
	}
	if ep.isOp("-") {
		pos := ep.peek().pos
		ep.i++
		x, err := ep.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryExpr{op: "-", x: x, pos: pos}, nil
	}
	return ep.parsePostfix()
}

// parsePostfix 处理管道：x|f(a) 改写成 f(x, a)。
func (ep *exprParser) parsePostfix() (expr, error) {
	x, err := ep.parsePrimary()
	if err != nil {
		return nil, err
	}
	for ep.isOp("|") {
		ep.i++
		t := ep.peek()
		if t == nil || t.kind != tIdent {
			return nil, ep.errHere("expected a function name after |")
		}
		if reserved[t.text] {
			return nil, ep.errHere("%q is a reserved word and cannot be used as a function", t.text)
		}
		ep.i++
		call := &callExpr{name: t.text, args: []expr{x}, pos: t.pos}
		if ep.isOp("(") {
			args, err := ep.parseArgs()
			if err != nil {
				return nil, err
			}
			call.args = append(call.args, args...)
		}
		x = call
	}
	return x, nil
}

func (ep *exprParser) parseArgs() ([]expr, error) {
	ep.i++ // '('
	var args []expr
	if ep.isOp(")") {
		ep.i++
		return args, nil
	}
	for {
		a, err := ep.parseOr()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if ep.isOp(",") {
			ep.i++
			continue
		}
		if ep.isOp(")") {
			ep.i++
			return args, nil
		}
		return nil, ep.errHere("expected ',' or ')'")
	}
}

func (ep *exprParser) parsePrimary() (expr, error) {
	t := ep.peek()
	if t == nil {
		return nil, ep.errHere("unexpected end of expression")
	}
	switch t.kind {
	case tString:
		ep.i++
		return &litExpr{val: t.str, pos: t.pos}, nil
	case tNumber:
		ep.i++
		return &litExpr{val: t.num, pos: t.pos}, nil
	case tPath:
		pe, err := parsePath(t.text)
		if err != nil {
			return nil, ep.p.errAt(t.pos, "%s", err.Error())
		}
		ep.i++
		return &pathRef{p: pe, pos: t.pos}, nil
	case tIdent:
		switch t.text {
		case "true":
			ep.i++
			return &litExpr{val: true, pos: t.pos}, nil
		case "false":
			ep.i++
			return &litExpr{val: false, pos: t.pos}, nil
		case "null":
			ep.i++
			return &litExpr{val: nil, pos: t.pos}, nil
		}
		if reserved[t.text] {
			return nil, ep.errHere("unexpected reserved word %q", t.text)
		}
		ep.i++
		if !ep.isOp("(") {
			return nil, ep.p.errAt(t.pos, "unexpected %q; variables start with $ and functions need ()", t.text)
		}
		args, err := ep.parseArgs()
		if err != nil {
			return nil, err
		}
		return &callExpr{name: t.text, args: args, pos: t.pos}, nil
	case tOp:
		switch t.text {
		case "(":
			ep.i++
			x, err := ep.parseOr()
			if err != nil {
				return nil, err
			}
			if !ep.isOp(")") {
				return nil, ep.errHere("expected ')'")
			}
			ep.i++
			return x, nil
		case "[":
			ep.i++
			arr := &arrayExpr{pos: t.pos}
			if ep.isOp("]") {
				ep.i++
				return arr, nil
			}
			for {
				it, err := ep.parseOr()
				if err != nil {
					return nil, err
				}
				arr.items = append(arr.items, it)
				if ep.isOp(",") {
					ep.i++
					continue
				}
				if ep.isOp("]") {
					ep.i++
					return arr, nil
				}
				return nil, ep.errHere("expected ',' or ']'")
			}
		case "{":
			ep.i++
			obj := &objectExpr{pos: t.pos}
			if ep.isOp("}") {
				ep.i++
				return obj, nil
			}
			for {
				k := ep.peek()
				if k == nil || (k.kind != tString && k.kind != tIdent) {
					return nil, ep.errHere("expected an object key")
				}
				key := k.str
				if k.kind == tIdent {
					key = k.text
				}
				ep.i++
				if !ep.isOp(":") {
					return nil, ep.errHere("expected ':' after object key")
				}
				ep.i++
				v, err := ep.parseOr()
				if err != nil {
					return nil, err
				}
				obj.keys = append(obj.keys, key)
				obj.vals = append(obj.vals, v)
				if ep.isOp(",") {
					ep.i++
					continue
				}
				if ep.isOp("}") {
					ep.i++
					return obj, nil
				}
				return nil, ep.errHere("expected ',' or '}'")
			}
		}
	}
	return nil, ep.errHere("unexpected %q", t.text)
}

package drusql

import (
	"fmt"
	"math"
	"strings"
)

type localVar struct {
	name string
	val  any
}

type execCtx struct {
	tpl    *Template
	eng    *Engine
	root   map[string]any
	locals []localVar
	sb     *strings.Builder
	args   []any
}

func newExecCtx(t *Template, data map[string]any) *execCtx {
	root := make(map[string]any, len(data)+1)
	for k, v := range data {
		root[k] = normalize(v)
	}
	return &execCtx{tpl: t, eng: t.eng, root: root, sb: &strings.Builder{}}
}

func (c *execCtx) errAt(pos int, format string, args ...any) error {
	line, col := lineCol(c.tpl.src, pos)
	return &ExecError{Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}
}

func (c *execCtx) lookupLocal(name string) any {
	for i := len(c.locals) - 1; i >= 0; i-- {
		if c.locals[i].name == name {
			return c.locals[i].val
		}
	}
	return Undefined
}

// ---- 渲染 ----

func (c *execCtx) render(nodes []node) error {
	for _, n := range nodes {
		switch x := n.(type) {
		case *textNode:
			c.sb.WriteString(x.text)
		case *valueNode:
			if err := c.emitValue(x); err != nil {
				return err
			}
		case *ifNode:
			if err := c.renderIf(x); err != nil {
				return err
			}
		case *forNode:
			if err := c.renderFor(x); err != nil {
				return err
			}
		case *setNode:
			if err := c.execSet(x); err != nil {
				return err
			}
		case *validateNode:
			// 已在第一步执行
		}
	}
	return nil
}

func (c *execCtx) bind(v any) {
	c.args = append(c.args, v)
	c.sb.WriteString(c.eng.dialect.placeholder(len(c.args)))
}

func (c *execCtx) emitValue(n *valueNode) error {
	v, err := c.eval(n.x)
	if err != nil {
		return err
	}
	switch x := v.(type) {
	case undefinedType:
		return c.errAt(n.pos, "value is undefined; check the path or add |default(...)")
	case rawSQL:
		c.sb.WriteString(string(x))
	case joined:
		for i, it := range x.items {
			if i > 0 {
				c.sb.WriteString(x.sep)
			}
			switch it.(type) {
			case []any, map[string]any:
				return c.errAt(n.pos, "join expects an array of scalars, element %d is %s", i, typeName(it))
			}
			c.bind(it)
		}
	case []any, map[string]any:
		return c.errAt(n.pos, "cannot bind %s as a single parameter; use |join(\",\") or a for loop", typeName(v))
	default:
		c.bind(v)
	}
	return nil
}

func (c *execCtx) renderIf(n *ifNode) error {
	for _, b := range n.branches {
		v, err := c.eval(b.cond)
		if err != nil {
			return err
		}
		ok, err := c.truth(v, b.cond.exprPos(), "if condition")
		if err != nil {
			return err
		}
		if ok {
			return c.render(b.body)
		}
	}
	return c.render(n.elseBody)
}

func (c *execCtx) renderFor(n *forNode) error {
	v, err := c.eval(n.x)
	if err != nil {
		return err
	}
	items, ok := v.([]any)
	if !ok {
		return c.errAt(n.pos, "for expects an array, got %s", typeName(v))
	}
	var saved map[string]any
	if n.hasSet {
		saved = c.snapshotVars()
	}
	outer := c.sb
	parts := make([]string, 0, len(items))
	for _, it := range items {
		c.sb = &strings.Builder{}
		c.locals = append(c.locals, localVar{n.varName, it})
		err := c.render(n.body)
		c.locals = c.locals[:len(c.locals)-1]
		if err != nil {
			c.sb = outer
			return err
		}
		parts = append(parts, strings.TrimSpace(c.sb.String()))
	}
	c.sb = outer
	if n.hasSet {
		c.restoreVars(saved)
	}
	// 每一项去掉首尾空白后，用 " sep " 连接；没有 sep 时用一个空格。
	glue := " "
	if n.hasSep {
		glue = " " + n.sep + " "
	}
	c.sb.WriteString(strings.Join(parts, glue))
	return nil
}

// snapshotVars 复制 $.request 以外的全部变量，用于让循环内的 set 只在循环内有效。
func (c *execCtx) snapshotVars() map[string]any {
	m := make(map[string]any, len(c.root))
	for k, v := range c.root {
		if k == "request" {
			continue
		}
		m[k] = normalize(v)
	}
	return m
}

func (c *execCtx) restoreVars(saved map[string]any) {
	for k := range c.root {
		if k != "request" {
			delete(c.root, k)
		}
	}
	for k, v := range saved {
		c.root[k] = v
	}
}

func (c *execCtx) execSet(n *setNode) error {
	v, err := c.eval(n.x)
	if err != nil {
		return err
	}
	switch v.(type) {
	case rawSQL, joined:
		return c.errAt(n.pos, "cannot assign %s to a variable", typeName(v))
	}
	m := c.root
	for _, name := range n.path[:len(n.path)-1] {
		next, ok := m[name].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[name] = next
		}
		m = next
	}
	last := n.path[len(n.path)-1]
	if isUndefined(v) {
		delete(m, last)
	} else {
		m[last] = v
	}
	return nil
}

// ---- 表达式求值 ----

// truth 把条件转成布尔值：只接受布尔值；undefined 视为 false。
func (c *execCtx) truth(v any, pos int, what string) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case undefinedType:
		return false, nil
	}
	return false, c.errAt(pos, "%s must be a boolean, got %s", what, typeName(v))
}

func (c *execCtx) eval(x expr) (any, error) {
	switch e := x.(type) {
	case *litExpr:
		return e.val, nil
	case *pathRef:
		var cur any
		if e.p.local != "" {
			cur = c.lookupLocal(e.p.local)
		}
		return e.p.value(c.root, cur), nil
	case *arrayExpr:
		out := make([]any, len(e.items))
		for i, it := range e.items {
			v, err := c.eval(it)
			if err != nil {
				return nil, err
			}
			if isUndefined(v) {
				return nil, c.errAt(it.exprPos(), "array element is undefined")
			}
			out[i] = v
		}
		return out, nil
	case *objectExpr:
		out := make(map[string]any, len(e.keys))
		for i, k := range e.keys {
			v, err := c.eval(e.vals[i])
			if err != nil {
				return nil, err
			}
			if isUndefined(v) {
				return nil, c.errAt(e.vals[i].exprPos(), "object value is undefined")
			}
			out[k] = v
		}
		return out, nil
	case *unaryExpr:
		v, err := c.eval(e.x)
		if err != nil {
			return nil, err
		}
		if e.op == "not" {
			b, err := c.truth(v, e.pos, "operand of not")
			if err != nil {
				return nil, err
			}
			return !b, nil
		}
		switch n := v.(type) {
		case int64:
			return -n, nil
		case float64:
			return -n, nil
		}
		return nil, c.errAt(e.pos, "operand of - must be a number, got %s", typeName(v))
	case *binaryExpr:
		return c.evalBinary(e)
	case *callExpr:
		def := c.eng.funcs[e.name]
		args := make([]any, len(e.args))
		for i, a := range e.args {
			v, err := c.eval(a)
			if err != nil {
				return nil, err
			}
			args[i] = v
		}
		v, err := def.fn(c, args)
		if err != nil {
			return nil, c.errAt(e.pos, "%s: %s", e.name, err.Error())
		}
		return v, nil
	}
	return nil, fmt.Errorf("drusql: unknown expression type %T", x)
}

func (c *execCtx) evalBinary(e *binaryExpr) (any, error) {
	switch e.op {
	case "and", "or":
		lv, err := c.eval(e.l)
		if err != nil {
			return nil, err
		}
		l, err := c.truth(lv, e.l.exprPos(), "operand of "+e.op)
		if err != nil {
			return nil, err
		}
		if e.op == "and" && !l {
			return false, nil
		}
		if e.op == "or" && l {
			return true, nil
		}
		rv, err := c.eval(e.r)
		if err != nil {
			return nil, err
		}
		return c.truth(rv, e.r.exprPos(), "operand of "+e.op)
	}
	l, err := c.eval(e.l)
	if err != nil {
		return nil, err
	}
	r, err := c.eval(e.r)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "==", "!=", ">", ">=", "<", "<=":
		// 与 undefined 的任何比较（包括 !=）结果都是 false。
		if isUndefined(l) || isUndefined(r) {
			return false, nil
		}
		switch e.op {
		case "==":
			return valuesEqual(l, r), nil
		case "!=":
			return !valuesEqual(l, r), nil
		}
		cmp, ok := valuesCompare(l, r)
		if !ok {
			return false, nil
		}
		switch e.op {
		case ">":
			return cmp > 0, nil
		case ">=":
			return cmp >= 0, nil
		case "<":
			return cmp < 0, nil
		default:
			return cmp <= 0, nil
		}
	case "~":
		ls, ok1 := concatStr(l)
		rs, ok2 := concatStr(r)
		if !ok1 || !ok2 {
			return nil, c.errAt(e.pos, "cannot concatenate %s and %s", typeName(l), typeName(r))
		}
		return ls + rs, nil
	}
	// 算术
	li, lIsInt := l.(int64)
	ri, rIsInt := r.(int64)
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if !lok || !rok {
		return nil, c.errAt(e.pos, "operands of %s must be numbers, got %s and %s", e.op, typeName(l), typeName(r))
	}
	bothInt := lIsInt && rIsInt
	switch e.op {
	case "+":
		if bothInt {
			return li + ri, nil
		}
		return lf + rf, nil
	case "-":
		if bothInt {
			return li - ri, nil
		}
		return lf - rf, nil
	case "*":
		if bothInt {
			return li * ri, nil
		}
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, c.errAt(e.pos, "division by zero")
		}
		if bothInt && li%ri == 0 {
			return li / ri, nil
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, c.errAt(e.pos, "division by zero")
		}
		if bothInt {
			return li % ri, nil
		}
		return math.Mod(lf, rf), nil
	}
	return nil, c.errAt(e.pos, "unknown operator %s", e.op)
}

func concatStr(v any) (string, bool) {
	if v == nil {
		return "", true
	}
	return toStr(v)
}

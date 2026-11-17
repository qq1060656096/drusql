// Package drusql 是一个 SQL 模板引擎：把带 {{ }} 占位符的模板编译成
// “SQL 文本 + 绑定参数列表”，支持 MySQL、PostgreSQL、Oracle 和 SQL Server。
//
// 引擎只负责绑定值和执行控制流（if / for / set / validate），不裁剪 SQL；
// SQL 的正确性由模板作者负责。完整语法见 README。
package drusql

import (
	"fmt"
	"strconv"
)

// Dialect 决定参数占位符的形式。
type Dialect int

const (
	MySQL      Dialect = iota // ?
	PostgreSQL                // $1
	Oracle                    // :1
	SQLServer                 // @p1
)

func (d Dialect) String() string {
	switch d {
	case MySQL:
		return "mysql"
	case PostgreSQL:
		return "postgresql"
	case Oracle:
		return "oracle"
	case SQLServer:
		return "sqlserver"
	}
	return "unknown"
}

func (d Dialect) placeholder(n int) string {
	switch d {
	case PostgreSQL:
		return "$" + strconv.Itoa(n)
	case Oracle:
		return ":" + strconv.Itoa(n)
	case SQLServer:
		return "@p" + strconv.Itoa(n)
	}
	return "?"
}

// Func 是自定义函数。管道左边的值作为第一个参数传入；
// 路径不存在时对应的参数是 Undefined。
type Func func(args []any) (any, error)

// Engine 持有方言和函数表。Parse 得到的模板可以并发执行；
// RegisterFunc 需要在 Parse 之前调用。
type Engine struct {
	dialect Dialect
	funcs   map[string]funcDef
}

// New 创建一个指定方言的引擎。
func New(d Dialect) *Engine {
	e := &Engine{dialect: d, funcs: make(map[string]funcDef, len(builtins))}
	for name, def := range builtins {
		e.funcs[name] = def
	}
	return e
}

// Dialect 返回引擎的方言。
func (e *Engine) Dialect() Dialect { return e.dialect }

// RegisterFunc 注册一个自定义函数，可以用 f(x, a) 或 x|f(a) 两种写法调用。
// maxArgs 为 -1 表示不限制参数个数。保留字和内置函数名不能使用。
func (e *Engine) RegisterFunc(name string, minArgs, maxArgs int, fn Func) error {
	if name == "" || !isIdentStart(name[0]) {
		return fmt.Errorf("drusql: invalid function name %q", name)
	}
	for i := 0; i < len(name); i++ {
		if !isIdentChar(name[i]) {
			return fmt.Errorf("drusql: invalid function name %q", name)
		}
	}
	if reserved[name] {
		return fmt.Errorf("drusql: %q is a reserved word", name)
	}
	if _, ok := builtins[name]; ok {
		return fmt.Errorf("drusql: %q is a built-in function", name)
	}
	if fn == nil {
		return fmt.Errorf("drusql: function %q is nil", name)
	}
	e.funcs[name] = funcDef{min: minArgs, max: maxArgs, fn: func(_ *execCtx, args []any) (any, error) {
		return fn(args)
	}}
	return nil
}

// Template 是解析后的模板，不可变，可以并发执行。
type Template struct {
	eng   *Engine
	src   string
	nodes []node
}

// Parse 解析模板。语法错误、未知函数等在这里报出，类型为 *ParseError。
func (e *Engine) Parse(src string) (*Template, error) {
	p := &tplParser{eng: e, src: src}
	if err := p.scan(); err != nil {
		return nil, err
	}
	nodes, end, endSeg, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	if end != "" {
		return nil, p.errAt(endSeg.pos, "unexpected {{%s}}", end)
	}
	return &Template{eng: e, src: src, nodes: nodes}, nil
}

// MustParse 与 Parse 相同，出错时 panic，适合初始化全局模板。
func (e *Engine) MustParse(src string) *Template {
	t, err := e.Parse(src)
	if err != nil {
		panic(err)
	}
	return t
}

// Result 是模板的编译结果，可以直接交给 database/sql：
//
//	db.Query(res.SQL, res.Args...)
type Result struct {
	SQL  string
	Args []any
}

// Request 构造模板的根数据：$.request.body 和 $.request.header。
func Request(body, header any) map[string]any {
	return map[string]any{"request": map[string]any{"body": body, "header": header}}
}

// Exec 执行模板。data 是根对象 $，通常用 Request(body, header) 构造。
//
// 执行分两步：先按书写顺序执行顶层的 set 和 validate 并收集全部校验失败项，
// 有失败项时返回 ValidationErrors，不渲染 SQL；全部通过后渲染 SQL。
// 其他执行期错误的类型为 *ExecError。
func (t *Template) Exec(data map[string]any) (*Result, error) {
	c := newExecCtx(t, data)
	initial := c.snapshotVars()
	verrs, err := c.runValidation()
	if err != nil {
		return nil, err
	}
	if len(verrs) > 0 {
		return nil, verrs
	}
	// 渲染时会按顺序重新执行全部 set，这里先还原变量，
	// 保证每条 set 在最终结果里只生效一次。
	c.restoreVars(initial)
	if err := c.render(t.nodes); err != nil {
		return nil, err
	}
	return &Result{SQL: c.sb.String(), Args: c.args}, nil
}

// Validate 只执行顶层的 set 和 validate，返回全部校验失败项，不渲染 SQL。
func (t *Template) Validate(data map[string]any) (ValidationErrors, error) {
	c := newExecCtx(t, data)
	return c.runValidation()
}

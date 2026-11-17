# drusql

[English](README.md) | 简体中文

drusql 是一个 Go 语言的 SQL 模板引擎：把带 `{{ }}` 占位符的模板编译成“SQL 文本 + 绑定参数列表”，支持 MySQL、PostgreSQL、Oracle、SQL Server。

- **安全**：请求里的值一律走参数绑定，不拼入 SQL 文本
- **够用**：只有 5 类标签——值占位符、`if`、`for`、`validate`、`set`
- **熟悉**：变量路径用 JSONPath，表达式写法参考 Twig，校验规则参考 Laravel
- **轻量**：零依赖，只用标准库；解析后的模板可以并发执行

## 目录

- [安装](#安装)
- [快速开始](#快速开始)
- [执行过程](#执行过程)
- [Go API](#go-api)
- [模板语法](#模板语法)
- [书写约定](#书写约定)
- [常见用法](#常见用法)
- [错误处理](#错误处理)
- [注意事项](#注意事项)
- [许可证](#许可证)

## 安装

```
go get github.com/qq1060656096/drusql
```

需要 Go 1.21 或更高版本。

## 快速开始

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qq1060656096/drusql"
)

// 模板只需解析一次，之后可以并发执行。
var eng = drusql.New(drusql.MySQL) // 或 PostgreSQL / Oracle / SQLServer

var listUsers = eng.MustParse(`{{validate $.request.body.ids rule="array|max:100" message="最多100个id"}}
select * from users
where 1=1
{{if notIsEmpty($.request.body.name)}}
  and name like {{$.request.body.name|trim|like}} escape '!'
{{/if}}
{{if notIsEmpty($.request.body.ids)}}
  and id in ({{$.request.body.ids|join(",")}})
{{/if}}
order by {{$.request.body.sort|default("id")|ident("id,created_at")}} desc`)

func main() {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"name": " Tom ", "ids": [1, 2, 3]}`), &body)

	res, err := listUsers.Exec(drusql.Request(body, nil))

	var verrs drusql.ValidationErrors
	switch {
	case errors.As(err, &verrs):
		// 校验失败：把 verrs 返回给前端
		out, _ := json.Marshal(map[string]any{"errors": verrs})
		fmt.Println(string(out))
	case err != nil:
		// 其他执行期错误
		fmt.Println(err)
	default:
		fmt.Println(res.SQL)
		fmt.Println(res.Args...)
		// rows, err := db.Query(res.SQL, res.Args...)
	}
}
```

输出：

```
select * from users
where 1=1
  and name like ? escape '!'
  and id in (?,?,?)
order by id desc
%Tom% 1 2 3
```

同一个模板换方言，只有占位符形式不同，参数完全一样：

| 方言 | 占位符 | 上例的 `in` 列表 |
| --- | --- | --- |
| `drusql.MySQL` | `?` | `in (?,?,?)` |
| `drusql.PostgreSQL` | `$n` | `in ($2,$3,$4)` |
| `drusql.Oracle` | `:n` | `in (:2,:3,:4)` |
| `drusql.SQLServer` | `@pn` | `in (@p2,@p3,@p4)` |

## 执行过程

`Exec` 分两步：

1. 按书写顺序执行顶层的 `set` 和 `validate`。校验失败后不停止，收集全部失败项。
2. 有失败项时返回 `ValidationErrors`，不渲染 SQL；全部通过后渲染 SQL，得到 `Result{SQL, Args}`。

所以 `validate` 无论写在模板的哪个位置，都先于 SQL 生效。

## Go API

| API | 说明 |
| --- | --- |
| `drusql.New(dialect) *Engine` | 创建引擎。方言：`MySQL`、`PostgreSQL`、`Oracle`、`SQLServer` |
| `(*Engine).Parse(src) (*Template, error)` | 解析模板，错误类型为 `*ParseError`（带行列号） |
| `(*Engine).MustParse(src) *Template` | 同上，出错时 panic，适合初始化全局模板 |
| `(*Engine).RegisterFunc(name, minArgs, maxArgs, fn) error` | 注册自定义函数，需在 `Parse` 之前调用 |
| `(*Template).Exec(data) (*Result, error)` | 执行模板，返回 `Result{SQL string, Args []any}` |
| `(*Template).Validate(data) (ValidationErrors, error)` | 只做校验，不渲染 SQL |
| `drusql.Request(body, header) map[string]any` | 构造根数据 `{"request": {"body": ..., "header": ...}}` |
| `drusql.Undefined` | 路径不存在时的值，自定义函数可用它判断参数是否缺失 |

`data` 是模板里的根对象 `$`。`body` 和 `header` 可以是解码后的 JSON，也可以是任意 Go 值（map、切片、结构体等，结构体字段名取 `json` 标签）。输入数据不会被修改。

除了 `request`，还可以往根对象里放其他数据，模板里用 `$.xxx` 访问：

```go
data := drusql.Request(body, header)
data["tenant"] = map[string]any{"id": 42}
// 模板：where tenant_id = {{$.tenant.id}}
```

## 模板语法

### 值占位符

```
{{$.request.body.name}}
{{$.request.body.name|trim|default("")}}
{{$.request.body.ids|join(",")}}
```

- 输出一个参数占位符，并把求值结果追加到参数列表。
- 结果是 `undefined`（路径不存在）时报错，需要先经过 `default`。
- 结果是 `null` 时绑定 NULL。注意 `col = NULL` 永不成立，判空请用 `if isNull(...)` 写 `col is null`。
- 数组不能直接绑定，用 `join` 或 `for` 展开。

### 变量路径（JSONPath）

| 写法 | 含义 |
| --- | --- |
| `$.request.body.name`、`$.request.body['X-Key']` | 取成员 |
| `$.request.body.users[0].id`、`[-1]` | 取下标，负数从末尾数 |
| `$.request.body.users[*].user_id` | 全部元素 |
| `$.request.body.ids[0:10]`、`[::2]` | 切片 |
| `$.request.body.users[?@.type == 1 && @.age > 18]` | 过滤，`@` 表示当前元素 |
| `$..user_id` | 递归查找 |
| `$v`、`$v.user_id` | 循环变量（对 JSONPath 的扩展） |

- 只含成员和下标的路径返回**单个值**，不存在时为 `undefined`。
- 含 `[*]`、切片、过滤或 `..` 的路径**始终返回数组**，没有匹配时为空数组。
- 过滤表达式不支持 RFC 9535 的函数扩展（`length()`、`match()` 等）。

### 函数与管道

管道和函数调用是同一回事：`x|f(a)` 等价于 `f(x, a)`，没有额外参数时管道可以省略括号。函数名区分大小写。

| 函数 | 作用 |
| --- | --- |
| `default(x, v)` | `isEmpty(x)` 为真时返回 `v` |
| `trim(x)` `lower(x)` `upper(x)` | 字符串处理；非字符串原样返回 |
| `length(x)` | 字符串字符数或数组元素个数；`undefined` 和 `null` 为 0 |
| `int(x)` `float(x)` `string(x)` `bool(x)` | 类型转换，转换失败报错 |
| `date(x, "Y-m-d H:i:s")` | 格式化日期，格式串为 PHP / Twig 写法，支持 `Y y m n d j H i s` |
| `join(x, ",")` | 把数组展开为多个绑定参数；必须在管道链末尾，分隔符必须是字面量 |
| `like(x)` `likeLeft(x)` `likeRight(x)` | 转义通配符后绑定 `%值%` / `%值` / `值%` |
| `ident(x, "a,b,c")` | 值在白名单内则作为标识符拼入 SQL，否则报错；白名单必须是字面量 |
| `raw(x)` | 原样拼入 SQL，不绑定；安全性由模板作者负责 |
| `isDefined(x)` | 路径存在（值为 null 也算存在） |
| `isNull(x)` | 路径存在且值为 null |
| `isEmpty(x)` | 未定义、null、空字符串、空数组；`0`、`"0"`、`false` 不算空 |
| `notIsEmpty(x)` | `isEmpty(x)` 的取反 |
| `t(key)` | 多语言文案，见下文 |

只有 `ident` 和 `raw` 会改变 SQL 文本，其余函数只改变绑定值。

`like` 系列用 `!` 作转义符，模板里要配套写 `escape '!'`：

```
name like {{$.request.body.kw|like}} escape '!'
```

自定义函数：

```go
eng := drusql.New(drusql.MySQL)
err := eng.RegisterFunc("mask", 1, 1, func(args []any) (any, error) {
	s, ok := args[0].(string)
	if !ok {
		return args[0], nil // Undefined、nil 等原样透传
	}
	if len(s) <= 3 {
		return s, nil
	}
	return s[:3] + "****", nil
})
// 模板：{{$.request.body.phone|mask}} 或 {{mask($.request.body.phone)}}
```

### 表达式

用在值占位符、`if` 条件、`set` 右侧和函数参数里。

- 逻辑：`and` / `&&`、`or` / `||`、`not` / `!`（两套写法等价）
- 比较：`==` `!=` `>` `>=` `<` `<=`
- 算术：`+` `-` `*` `/` `%`；字符串拼接：`~`
- 字面量：`"abc"` / `'abc'`、`100`、`1.5`、`true`、`false`、`null`、数组 `[1, 2]`、对象 `{"k": "v"}`
- 优先级从高到低：管道与函数调用、`not`、算术、比较、`and`、`or`；可用括号改变

求值规则：

- 与 `undefined` 的任何比较（包括 `!=`）都是 false，所以 `$.request.body.id > 0` 在字段缺失时直接为假。
- `if`、`and`、`or`、`not` 的操作数必须是布尔值，否则报错；`undefined` 视为 false。
- 数字与数字形式的字符串按数值比较（`"5" == 5` 为真）。
- 判断“有值”统一写 `notIsEmpty(x)`，不要依赖 `x != ""`。

### 条件

```
{{if 表达式}}
  ...
{{elseif 表达式}}
  ...
{{else}}
  ...
{{/if}}
```

只输出第一个命中的分支。未命中分支里的表达式不求值，所以其中的 `undefined` 不会报错。可以嵌套。

### 循环

```
{{for v in $.request.body.users sep "or"}}
  (user_id = {{$v.user_id}} and type = {{$v.type}})
{{/for}}
```

输出：

```
(user_id = ? and type = ?) or (user_id = ? and type = ?)
```

- 每一项去掉首尾空白后用 ` sep ` 连接；`sep` 可以省略，省略时用一个空格连接。
- `sep` 只接受字符串字面量。
- 循环变量用 `$v` 引用，循环外不可见。
- 空数组输出为空；被遍历的值不是数组（包括 `undefined`）时报错。
- 可以嵌套，循环体内可以使用 `if`。

### 校验

```
{{validate $.request.body.name rule="required|string|max:100" message="请填写名称"}}
{{validate $.request.body.users rule="required|array|max:100" message="用户列表必填，最多100个"}}
{{validate $.request.body.users[*].type rule="in:1,2,3" message=t("bad_type")}}
```

规则写法与 Laravel 验证器一致：多条规则用 `|` 分隔，参数用 `:` 引出。

| 规则 | 含义 |
| --- | --- |
| `required` | 字段必须存在且不为空（按 `isEmpty` 判断） |
| `string` `int` `numeric` `bool` `array` `date` | 类型检查；`int` 和 `numeric` 接受数字形式的字符串 |
| `min:n` `max:n` `between:a,b` | 字符串按字符数、数字按大小、数组按元素个数 |
| `in:a,b,c` | 值必须在列表内 |
| `regex:模式` | 匹配正则；模式里可能含有竖线，所以必须写在规则链最后 |

规则说明：

- 值为未定义、null 或空字符串时，除 `required` 外的规则都跳过。
- 一条 `validate` 对一个值最多报一个错（规则链中第一条没通过的）。想让不同规则显示不同文案，就对同一字段写多条 `validate`。
- 路径里的 `[*]` 按父数组下标展开，逐个元素校验；元素缺少该字段时 `required` 能检出。
- `validate` 的路径只支持成员、下标和 `[*]`，并且只能写在模板顶层。
- `message` 是表达式，可以是字符串，也可以是 `t("key")`；省略时使用默认文案。

校验失败时 `Exec` 返回 `drusql.ValidationErrors`，序列化后是：

```json
{
  "errors": [
    { "field": "$.request.body.name", "rule": "required", "message": "请填写名称" },
    { "field": "$.request.body.users[1].type", "rule": "in", "message": "用户类型不合法" }
  ]
}
```

`field` 是出错值的完整 JSONPath，`[*]` 已替换为具体下标，前端可以用它定位到输入框或列表中的某一行。

### 变量

```
{{set $.page = $.request.body.page|default(1)|int}}
{{set $.offset = ($.page - 1) * 20}}
```

- `set` 只有“路径 = 表达式”一种形式，不输出 SQL。默认值用 `default` 函数实现。
- `$.request` 只读，对它赋值会在加载模板时报错。
- 中间层级不存在时自动创建。
- 循环内的 `set` 只在循环内有效。

### 多语言

文案放在 `$.lang.data.<语言>.<key>`，当前语言放在 `$.lang.current`，用 `t("key")` 取：

```
{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.name_required = "name is required"}}
{{set $.lang.data.zh.name_required = "请填写名称"}}

{{validate $.request.body.name rule="required" message=t("name_required")}}
```

也可以在一条 `set` 里定义完（结尾是三个 `}`：一个闭合对象，两个闭合标签）：

```
{{set $.lang = {
  "current": $.request.header.lang|default("en"),
  "default": "en",
  "data": {
    "en": {"name_required": "name is required"},
    "zh": {"name_required": "请填写名称"}
  }
}}}
```

`t(key)` 的查找顺序：

1. `$.lang.data.<当前语言>.<key>`
2. 回退到默认语言：`$.lang.default` 指定的语言，未设置时为 `en`
3. 仍找不到时返回 key 本身

文案也可以从 Go 代码里统一传入，模板里就不用写 `set`：

```go
data := drusql.Request(body, header)
data["lang"] = map[string]any{
	"current": "zh",
	"data": map[string]any{
		"en": map[string]any{"name_required": "name is required"},
		"zh": map[string]any{"name_required": "请填写名称"},
	},
}
res, err := tpl.Exec(data)
```

### 注释与字面量花括号

```
{# 这是注释，不会输出 #}
{{"{{"|raw}}      输出字面量 {{
```

独占一行的 `if` / `for` / `set` / `validate` 标签和注释不会在输出的 SQL 里留下空行。

## 书写约定

引擎不裁剪 SQL：不删除多余的 `and` / `or`，不补括号，模板写什么就输出什么。以下约定由模板作者遵守。

**1. and 条件以 `where 1=1` 开头**，每个可选条件自带 `and`：

```
where 1=1
{{if notIsEmpty($.request.body.id)}} and id = {{$.request.body.id}} {{/if}}
{{if notIsEmpty($.request.body.type)}} and type = {{$.request.body.type}} {{/if}}
```

**2. or 分组以 `(1=0` 开头**，每个可选条件自带 `or`：

```
and (1=0
  {{if notIsEmpty($.request.body.id)}} or id = {{$.request.body.id}} {{/if}}
  {{if notIsEmpty($.request.body.id2)}} or id = {{$.request.body.id2}} {{/if}}
)
```

两个条件都缺失时这个分组恒为假。如果期望“都缺失时不过滤”，在外层再包一个 `if`。

**3. `for` 整体包在括号里，外层用 `if` 判空**，否则会有优先级问题，空数组还会产生 `and ()`：

```
{{if notIsEmpty($.request.body.users)}}
  and ({{for v in $.request.body.users sep "or"}}
    (user_id = {{$v.user_id}} and type = {{$v.type}})
  {{/for}})
{{/if}}
```

**4. `in` 列表同样用 `if` 判空**，避免输出 `in ()`：

```
{{if notIsEmpty($.request.body.ids)}}
  and id in ({{$.request.body.ids|join(",")}})
{{/if}}
```

**5. 循环和 `join` 用到的数组必须限制长度**，数据库对单条语句的参数个数有上限：

```
{{validate $.request.body.users rule="array|max:100" message="最多100个用户"}}
```

**6. UPDATE / DELETE 的定位条件用 `validate` 设为必填，不要放在 `if` 里。** `where 1=1` 永远合法，条件全部被跳过时引擎无法察觉，语句会作用于全表。

## 常见用法

### 动态排序与分页

```
{{validate $.request.body.page rule="int|min:1" message="page 必须 >= 1"}}
{{validate $.request.body.size rule="int|between:1,100" message="size 必须在 1-100 之间"}}
select id, name from users
where 1=1
order by {{$.request.body.sort|default("id")|ident("id,name,created_at")}} {{$.request.body.dir|default("desc")|lower|ident("asc,desc")}}
limit {{$.request.body.size|default(20)|int}} offset {{($.request.body.page|default(1)|int - 1) * ($.request.body.size|default(20)|int)}}
```

列名和排序方向无法绑定，必须用 `ident` 限定在白名单内。分页语法各数据库不同，由模板作者按方言书写，例如 SQL Server：

```
order by id offset {{($.request.body.page|default(1)|int - 1) * 20}} rows fetch next {{20}} rows only
```

### 取数组某一列做 IN 查询

```
{{if notIsEmpty($.request.body.users[*].user_id)}}
  and user_id in ({{$.request.body.users[*].user_id|join(",")}})
{{/if}}
```

### 批量插入

```
{{validate $.request.body.users rule="required|array|between:1,100" message="需要 1-100 个用户"}}
insert into user_tag (user_id, type) values
{{for u in $.request.body.users sep ","}}
  ({{$u.user_id}}, {{$u.type|default(1)}})
{{/for}}
```

### 更新

```
{{validate $.request.body.id rule="required|int|min:1" message="id 必填"}}
update users
set name = {{$.request.body.name}}, updated_at = {{"now()"|raw}}
where id = {{$.request.body.id}}
```

### 在 HTTP 处理函数里使用

```go
func handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	header := map[string]any{"lang": r.Header.Get("X-Lang")}

	res, err := listUsers.Exec(drusql.Request(body, header))

	var verrs drusql.ValidationErrors
	switch {
	case errors.As(err, &verrs):
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{"errors": verrs})
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rows, err := db.QueryContext(r.Context(), res.SQL, res.Args...)
	// ...
}
```

## 错误处理

| 类型 | 何时出现 | 例子 |
| --- | --- | --- |
| `*drusql.ParseError` | `Parse` 时 | 标签未闭合、未知函数、`sep` 不是字面量、对 `$.request` 赋值 |
| `drusql.ValidationErrors` | `Exec` / `Validate` 时 | `validate` 没通过 |
| `*drusql.ExecError` | `Exec` 时 | `undefined` 落到值占位符、`ident` 的值不在白名单内、`for` 遍历的不是数组、类型转换失败 |

`ParseError` 和 `ExecError` 都带 `Line`、`Col` 和 `Msg`，指向模板中的位置。任何错误都不会产生 SQL。

```go
_, err := eng.Parse("select {{$.request.body.id|nosuch}}")
var pe *drusql.ParseError
if errors.As(err, &pe) {
	fmt.Println(pe.Line, pe.Col, pe.Msg) // 1 28 unknown function "nosuch"
}
```

## 注意事项

- `RegisterFunc` 要在 `Parse` 之前调用；`Template` 本身不可变，可以在多个 goroutine 里同时 `Exec`。
- JSON 解码得到的整数值（`float64` 且无小数）绑定为 `int64`。
- `{#` 在模板里表示注释开始，`{{` 表示标签开始；SQL 文本里需要这两个字面量时用 `{{"{{"|raw}}` 的方式输出。
- 引擎不检查 SQL 语法，也不知道语句是读还是写；UPDATE / DELETE 的保护依靠 `validate`。
- 暂不支持从数据库加载多语言数据（`loaddb`）。

## 许可证

[Apache License 2.0](LICENSE)

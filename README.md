# drusql

SQL 模板引擎：把带 `{{ }}` 占位符的模板编译成“SQL 文本 + 绑定参数列表”，支持 MySQL、PostgreSQL、Oracle、SQL Server。

- 请求里的值一律走参数绑定，不拼入 SQL 文本
- 5 类标签：值占位符、`if`、`for`、`validate`、`set`
- 变量路径采用 JSONPath，表达式写法参考 Twig，校验规则参考 Laravel
- 零依赖，只用标准库；解析后的模板可并发执行

```
go get github.com/qq1060656096/drusql
```

## 快速开始

```go
eng := drusql.New(drusql.MySQL) // 或 PostgreSQL / Oracle / SQLServer

tpl, err := eng.Parse(`
{{validate $.request.body.ids rule="array|max:100" message="最多100个id"}}
select * from users
where 1=1
{{if notIsEmpty($.request.body.name)}}
  and name like {{$.request.body.name|trim|like}} escape '!'
{{/if}}
{{if notIsEmpty($.request.body.ids)}}
  and id in ({{$.request.body.ids|join(",")}})
{{/if}}
order by {{$.request.body.sort|default("id")|ident("id,created_at")}} desc`)
if err != nil {
    // *drusql.ParseError：语法错误、未知函数等，带行列号
}

res, err := tpl.Exec(drusql.Request(body, header)) // body、header 是解码后的 JSON 或任意 Go 值
var verrs drusql.ValidationErrors
switch {
case errors.As(err, &verrs):
    // 校验失败，返回给前端：json.Marshal(map[string]any{"errors": verrs})
case err != nil:
    // *drusql.ExecError：与请求数据有关的执行期错误
default:
    rows, err := db.Query(res.SQL, res.Args...)
}
```

上面的模板在 `{"name": " Tom ", "ids": [1,2,3]}` 下得到：

```sql
select * from users
where 1=1
  and name like ? escape '!'
  and id in (?,?,?)
order by id desc
```

参数为 `["%Tom%", 1, 2, 3]`。换成 PostgreSQL 占位符是 `$1…$4`，Oracle 是 `:1…:4`，SQL Server 是 `@p1…@p4`。

## 语法

### 值占位符

```
{{$.request.body.name}}
{{$.request.body.name|trim|default("")}}
{{$.request.body.ids|join(",")}}
```

输出一个参数占位符并绑定求值结果。结果是 `undefined`（路径不存在）时报错，需要先经过 `default`；结果是 `null` 时绑定 NULL。

### 变量路径（JSONPath）

| 写法 | 含义 |
| --- | --- |
| `$.request.body.name`、`$.request.body['X-Key']` | 取成员 |
| `$.request.body.users[0].id`、`[-1]` | 取下标 |
| `$.request.body.users[*].user_id` | 全部元素 |
| `$.request.body.ids[0:10]`、`[::2]` | 切片 |
| `$.request.body.users[?@.type == 1 && @.age > 18]` | 过滤 |
| `$..user_id` | 递归查找 |
| `$v`、`$v.user_id` | 循环变量（对 JSONPath 的扩展） |

只含成员和下标的路径返回单个值，不存在时为 `undefined`；含 `[*]`、切片、过滤或 `..` 的路径始终返回数组。过滤表达式不支持 RFC 9535 的函数扩展（`length()`、`match()` 等）。

### 函数与管道

`x|f(a)` 等价于 `f(x, a)`。

| 函数 | 作用 |
| --- | --- |
| `default(x, v)` | `isEmpty(x)` 时取 `v` |
| `trim` `lower` `upper` `length` | 字符串处理；`length` 也可取数组长度 |
| `int` `float` `string` `bool` `date(x, "Y-m-d")` | 类型转换，失败报错 |
| `join(x, ",")` | 数组展开为多个绑定参数，必须在管道链末尾 |
| `like` `likeLeft` `likeRight` | 转义通配符后绑定 `%值%` / `%值` / `值%`，转义符为 `!`，模板里配套写 `escape '!'` |
| `ident(x, "a,b,c")` | 值在白名单内则作为标识符拼入 SQL，否则报错 |
| `raw(x)` | 原样拼入 SQL，不绑定，安全性由模板作者负责 |
| `isDefined` `isNull` `isEmpty` `notIsEmpty` | 判断；`isEmpty` 对未定义、null、`""`、`[]` 为真，`0` 和 `"0"` 不算空；`notIsEmpty` 是它的取反 |
| `t(key)` | 多语言文案 |

自定义函数：

```go
eng.RegisterFunc("mask", 1, 1, func(args []any) (any, error) { ... }) // 在 Parse 之前注册
```

### 表达式

- 逻辑：`and` / `&&`、`or` / `||`、`not` / `!`
- 比较：`==` `!=` `>` `>=` `<` `<=`
- 算术：`+` `-` `*` `/` `%`，字符串拼接 `~`
- 字面量：字符串、数字、`true` `false` `null`、数组 `[1, 2]`、对象 `{"k": "v"}`
- 优先级从高到低：管道与函数调用、`not`、算术、比较、`and`、`or`

与 `undefined` 的任何比较（包括 `!=`）都是 false。判断“有值”统一写 `notIsEmpty(x)`。

### 条件与循环

```
{{if 表达式}} ... {{elseif 表达式}} ... {{else}} ... {{/if}}

{{for v in $.request.body.users sep "or"}}
  (user_id = {{$v.user_id}} and type = {{$v.type}})
{{/for}}
```

`for` 的每一项去掉首尾空白后用 ` sep ` 连接，上例输出 `(user_id = ? and type = ?) or (user_id = ? and type = ?)`。`sep` 只接受字符串字面量。空数组输出为空，被遍历的值不是数组时报错。

### 校验

```
{{validate $.request.body.users rule="required|array|max:100" message="用户列表必填，最多100个"}}
{{validate $.request.body.users[*].type rule="in:1,2,3" message=t("bad_type")}}
```

- 规则：`required` `string` `int` `numeric` `bool` `array` `date` `min:n` `max:n` `between:a,b` `in:a,b,c` `regex:模式`
- 所有 `validate` 在渲染 SQL 之前执行，失败后不停止，全部失败项一次返回
- 每项含 `field`（完整 JSONPath，`[*]` 替换为具体下标）、`rule`、`message`
- 值为未定义、null 或空字符串时，除 `required` 外的规则都跳过
- `[*]` 按父数组下标展开，元素缺少该字段时 `required` 能检出
- `validate` 只能写在顶层；`regex` 的模式里可能有 `|`，必须写在规则链最后

### 变量与多语言

```
{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.name = "name not found"}}
{{set $.lang.data.zh.name = "名称不存在"}}
{{validate $.request.body.name rule="required" message=t("name")}}
```

`$.request` 只读。`t(key)` 先查当前语言，找不到回退到 `$.lang.default`（未设置时为 `en`），仍找不到返回 key 本身。循环内的 `set` 只在循环内有效。

### 注释与字面量花括号

```
{# 这是注释，不会输出 #}
{{"{{"|raw}}      输出字面量 {{
```

## 书写约定

引擎不裁剪 SQL，以下由模板作者负责：

1. and 条件以 `where 1=1` 开头，每个可选条件自带 `and`
2. or 分组以 `(1=0` 开头，每个可选条件自带 `or`
3. `for` 整体包在括号里，外层用 `if notIsEmpty(...)` 判空
4. `in ({{...|join(",")}})` 同样用 `if` 判空
5. 循环和 `join` 用到的数组用 `validate ... rule="array|max:N"` 限制长度
6. UPDATE / DELETE 的定位条件用 `validate` 设为必填，不要放在 `if` 里

独占一行的 `if` / `for` / `set` / `validate` 标签和注释不会在输出里留下空行。

## 实现说明

规范里几处未定的地方，目前按以下方式实现：

- `if`、`and`、`or`、`not` 的操作数必须是布尔值，否则报错；`undefined` 视为 false
- 数字与数字形式的字符串比较时按数值比较（`"5" == 5` 为真）
- `trim` / `lower` / `upper` 遇到非字符串原样返回，多数函数对 `undefined` 和 `null` 原样透传
- JSON 解码得到的整数值（`float64` 且无小数）绑定为 `int64`
- 输入数据不会被修改；结构体按其 JSON 形式参与路径访问
- 暂不支持 `loaddb`

## 测试

```
go test -race ./...
```

## License

[Apache License 2.0](LICENSE)

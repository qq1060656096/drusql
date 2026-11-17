# drusql

English | [简体中文](README.zh-CN.md)

drusql is a SQL template engine for Go. It compiles a template with `{{ }}` placeholders into SQL text plus a list of bound arguments, for MySQL, PostgreSQL, Oracle and SQL Server.

- **Safe**: request values are always bound as parameters, never concatenated into the SQL text
- **Small**: five kinds of tags — value placeholders, `if`, `for`, `validate`, `set`
- **Familiar**: JSONPath for variables, Twig-style expressions, Laravel-style validation rules
- **Light**: no dependencies beyond the standard library; parsed templates are safe for concurrent use

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [How execution works](#how-execution-works)
- [Go API](#go-api)
- [Template syntax](#template-syntax)
- [Writing conventions](#writing-conventions)
- [Recipes](#recipes)
- [Error handling](#error-handling)
- [Notes](#notes)
- [License](#license)

## Install

```
go get github.com/qq1060656096/drusql
```

Requires Go 1.21 or later.

## Quick start

```go
package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qq1060656096/drusql"
)

// Parse a template once; it can then be executed concurrently.
var eng = drusql.New(drusql.MySQL) // or PostgreSQL / Oracle / SQLServer

var listUsers = eng.MustParse(`{{validate $.request.body.ids rule="array|max:100" message="at most 100 ids"}}
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
		// Validation failed: send verrs back to the client.
		out, _ := json.Marshal(map[string]any{"errors": verrs})
		fmt.Println(string(out))
	case err != nil:
		// Any other execution error.
		fmt.Println(err)
	default:
		fmt.Println(res.SQL)
		fmt.Println(res.Args...)
		// rows, err := db.Query(res.SQL, res.Args...)
	}
}
```

Output:

```
select * from users
where 1=1
  and name like ? escape '!'
  and id in (?,?,?)
order by id desc
%Tom% 1 2 3
```

The same template works for every dialect. Only the placeholder form changes; the arguments are identical.

| Dialect | Placeholder | The `in` list above |
| --- | --- | --- |
| `drusql.MySQL` | `?` | `in (?,?,?)` |
| `drusql.PostgreSQL` | `$n` | `in ($2,$3,$4)` |
| `drusql.Oracle` | `:n` | `in (:2,:3,:4)` |
| `drusql.SQLServer` | `@pn` | `in (@p2,@p3,@p4)` |

## How execution works

`Exec` runs in two steps:

1. Top-level `set` and `validate` tags run in the order they are written. A failed validation does not stop the run; all failures are collected.
2. If anything failed, `Exec` returns `ValidationErrors` and renders no SQL. Otherwise it renders the SQL and returns `Result{SQL, Args}`.

A `validate` tag therefore takes effect before the SQL, wherever it sits in the template.

## Go API

| API | Description |
| --- | --- |
| `drusql.New(dialect) *Engine` | Creates an engine. Dialects: `MySQL`, `PostgreSQL`, `Oracle`, `SQLServer` |
| `(*Engine).Parse(src) (*Template, error)` | Parses a template. Errors are `*ParseError` with line and column |
| `(*Engine).MustParse(src) *Template` | Like `Parse` but panics on error; handy for package-level templates |
| `(*Engine).RegisterFunc(name, minArgs, maxArgs, fn) error` | Registers a custom function. Call it before `Parse` |
| `(*Template).Exec(data) (*Result, error)` | Executes the template and returns `Result{SQL string, Args []any}` |
| `(*Template).Validate(data) (ValidationErrors, error)` | Runs validation only, without rendering SQL |
| `drusql.Request(body, header) map[string]any` | Builds the root data `{"request": {"body": ..., "header": ...}}` |
| `drusql.Undefined` | The value of a path that does not exist; custom functions can test for it |

`data` is the root object `$` of the template. `body` and `header` can be decoded JSON or any Go value (maps, slices, structs; struct fields are addressed by their `json` tag). The input is never modified.

You can put more than `request` into the root object and read it as `$.xxx`:

```go
data := drusql.Request(body, header)
data["tenant"] = map[string]any{"id": 42}
// template: where tenant_id = {{$.tenant.id}}
```

## Template syntax

### Value placeholders

```
{{$.request.body.name}}
{{$.request.body.name|trim|default("")}}
{{$.request.body.ids|join(",")}}
```

- Emits one parameter placeholder and appends the evaluated value to the argument list.
- An `undefined` result (the path does not exist) is an error; pass it through `default` first.
- A `null` result binds NULL. Remember that `col = NULL` is never true; use `if isNull(...)` and write `col is null`.
- An array cannot be bound directly; expand it with `join` or `for`.

### Variable paths (JSONPath)

| Syntax | Meaning |
| --- | --- |
| `$.request.body.name`, `$.request.body['X-Key']` | Member access |
| `$.request.body.users[0].id`, `[-1]` | Index; negative counts from the end |
| `$.request.body.users[*].user_id` | All elements |
| `$.request.body.ids[0:10]`, `[::2]` | Slice |
| `$.request.body.users[?@.type == 1 && @.age > 18]` | Filter; `@` is the current element |
| `$..user_id` | Recursive descent |
| `$v`, `$v.user_id` | Loop variable (an extension to JSONPath) |

- A path made only of members and indexes returns a **single value**, or `undefined` if it does not exist.
- A path with `[*]`, a slice, a filter or `..` **always returns an array**, empty when nothing matches.
- Filter expressions do not support the RFC 9535 function extensions (`length()`, `match()` and so on).

### Functions and pipes

A pipe is a function call: `x|f(a)` is the same as `f(x, a)`, and the parentheses can be dropped when there are no extra arguments. Function names are case-sensitive.

| Function | What it does |
| --- | --- |
| `default(x, v)` | Returns `v` when `isEmpty(x)` is true |
| `trim(x)` `lower(x)` `upper(x)` | String helpers; non-strings pass through unchanged |
| `length(x)` | Character count of a string or element count of an array; 0 for `undefined` and `null` |
| `int(x)` `float(x)` `string(x)` `bool(x)` | Type conversion; fails with an error if the value cannot be converted |
| `date(x, "Y-m-d H:i:s")` | Formats a date using PHP / Twig format characters `Y y m n d j H i s` |
| `join(x, ",")` | Expands an array into several bound parameters; must be last in the pipe chain, and the separator must be a literal |
| `like(x)` `likeLeft(x)` `likeRight(x)` | Escapes wildcards and binds `%value%` / `%value` / `value%` |
| `ident(x, "a,b,c")` | Writes the value into the SQL as an identifier if it is in the whitelist, otherwise fails; the whitelist must be a literal |
| `raw(x)` | Writes the value into the SQL as is, unbound; the template author is responsible for its safety |
| `isDefined(x)` | The path exists (a null value counts as existing) |
| `isNull(x)` | The path exists and its value is null |
| `isEmpty(x)` | Undefined, null, empty string or empty array; `0`, `"0"` and `false` are not empty |
| `notIsEmpty(x)` | The negation of `isEmpty(x)` |
| `t(key)` | Localized message, see below |

Only `ident` and `raw` change the SQL text. Every other function only changes the bound value.

The `like` family uses `!` as the escape character, so write `escape '!'` next to it:

```
name like {{$.request.body.kw|like}} escape '!'
```

Custom functions:

```go
eng := drusql.New(drusql.MySQL)
err := eng.RegisterFunc("mask", 1, 1, func(args []any) (any, error) {
	s, ok := args[0].(string)
	if !ok {
		return args[0], nil // pass Undefined, nil etc. through
	}
	if len(s) <= 3 {
		return s, nil
	}
	return s[:3] + "****", nil
})
// template: {{$.request.body.phone|mask}} or {{mask($.request.body.phone)}}
```

### Expressions

Used in value placeholders, `if` conditions, the right-hand side of `set`, and function arguments.

- Logic: `and` / `&&`, `or` / `||`, `not` / `!` (the two spellings are equivalent)
- Comparison: `==` `!=` `>` `>=` `<` `<=`
- Arithmetic: `+` `-` `*` `/` `%`; string concatenation: `~`
- Literals: `"abc"` / `'abc'`, `100`, `1.5`, `true`, `false`, `null`, arrays `[1, 2]`, objects `{"k": "v"}`
- Precedence, highest first: pipes and function calls, `not`, arithmetic, comparison, `and`, `or`; use parentheses to override

Evaluation rules:

- Any comparison with `undefined`, including `!=`, is false. So `$.request.body.id > 0` is simply false when the field is missing.
- Operands of `if`, `and`, `or` and `not` must be booleans, otherwise it is an error; `undefined` counts as false.
- A number and a numeric string compare by value (`"5" == 5` is true).
- To test "has a value", write `notIsEmpty(x)` rather than `x != ""`.

### Conditions

```
{{if expr}}
  ...
{{elseif expr}}
  ...
{{else}}
  ...
{{/if}}
```

Only the first matching branch is emitted. Expressions in skipped branches are not evaluated, so an `undefined` there is not an error. Conditions can be nested.

### Loops

```
{{for v in $.request.body.users sep "or"}}
  (user_id = {{$v.user_id}} and type = {{$v.type}})
{{/for}}
```

Output:

```
(user_id = ? and type = ?) or (user_id = ? and type = ?)
```

- Each item is trimmed and the items are joined with ` sep `. `sep` is optional; without it items are joined with a single space.
- `sep` must be a string literal.
- The loop variable is referenced as `$v` and is not visible outside the loop.
- An empty array emits nothing. Looping over a non-array (including `undefined`) is an error.
- Loops can be nested and can contain `if`.

### Validation

```
{{validate $.request.body.name rule="required|string|max:100" message="name is required"}}
{{validate $.request.body.users rule="required|array|max:100" message="1 to 100 users required"}}
{{validate $.request.body.users[*].type rule="in:1,2,3" message=t("bad_type")}}
```

Rules follow the Laravel validator format: rules are separated by `|` and arguments follow a `:`.

| Rule | Meaning |
| --- | --- |
| `required` | The field must exist and not be empty (as defined by `isEmpty`) |
| `string` `int` `numeric` `bool` `array` `date` | Type checks; `int` and `numeric` accept numeric strings |
| `min:n` `max:n` `between:a,b` | Character count for strings, value for numbers, element count for arrays |
| `in:a,b,c` | The value must be in the list |
| `regex:pattern` | Must match the pattern. A pattern may contain a vertical bar, so `regex` must be the last rule |

Details:

- When the value is undefined, null or an empty string, every rule except `required` is skipped.
- One `validate` tag reports at most one error per value: the first rule in the chain that fails. To show different messages for different rules, write several `validate` tags for the same field.
- `[*]` in the path is expanded by the index of the parent array and each element is validated separately; `required` detects an element that lacks the field.
- A `validate` path supports members, indexes and `[*]` only, and `validate` must be at the top level of the template.
- `message` is an expression: a string or `t("key")`. A default message is used when it is omitted.

When validation fails, `Exec` returns `drusql.ValidationErrors`, which serializes to:

```json
{
  "errors": [
    { "field": "$.request.body.name", "rule": "required", "message": "name is required" },
    { "field": "$.request.body.users[1].type", "rule": "in", "message": "invalid user type" }
  ]
}
```

`field` is the full JSONPath of the failing value with `[*]` replaced by the actual index, so a front end can locate the input, or the row in a list.

### Variables

```
{{set $.page = $.request.body.page|default(1)|int}}
{{set $.offset = ($.page - 1) * 20}}
```

- `set` has one form, `path = expression`, and emits no SQL. Use the `default` function for default values.
- `$.request` is read-only; assigning to it is a parse error.
- Missing intermediate levels are created automatically.
- A `set` inside a loop only takes effect inside that loop.

### Localization

Messages live under `$.lang.data.<language>.<key>`, the current language under `$.lang.current`, and `t("key")` looks a message up:

```
{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.name_required = "name is required"}}
{{set $.lang.data.zh.name_required = "请填写名称"}}

{{validate $.request.body.name rule="required" message=t("name_required")}}
```

Everything can also go into a single `set` (note the three closing braces: one closes the object, two close the tag):

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

`t(key)` looks up in this order:

1. `$.lang.data.<current language>.<key>`
2. The fallback language: the one named by `$.lang.default`, or `en` if that is not set
3. The key itself

Messages can also be supplied from Go, so templates need no `set` at all:

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

### Comments and literal braces

```
{# a comment; emits nothing #}
{{"{{"|raw}}      emits a literal {{
```

An `if` / `for` / `set` / `validate` tag or a comment that stands alone on its line leaves no blank line in the generated SQL.

## Writing conventions

The engine does not trim SQL. It removes no dangling `and` / `or` and adds no parentheses; what the template says is what you get. The template author follows these conventions.

**1. Start `and` conditions with `where 1=1`**, and let every optional condition carry its own `and`:

```
where 1=1
{{if notIsEmpty($.request.body.id)}} and id = {{$.request.body.id}} {{/if}}
{{if notIsEmpty($.request.body.type)}} and type = {{$.request.body.type}} {{/if}}
```

**2. Start an `or` group with `(1=0`**, and let every optional condition carry its own `or`:

```
and (1=0
  {{if notIsEmpty($.request.body.id)}} or id = {{$.request.body.id}} {{/if}}
  {{if notIsEmpty($.request.body.id2)}} or id = {{$.request.body.id2}} {{/if}}
)
```

When both conditions are missing the group is always false. If you want "no filter when both are missing", wrap the group in an outer `if`.

**3. Wrap a `for` in parentheses and guard it with `if`.** Otherwise operator precedence goes wrong, and an empty array produces `and ()`:

```
{{if notIsEmpty($.request.body.users)}}
  and ({{for v in $.request.body.users sep "or"}}
    (user_id = {{$v.user_id}} and type = {{$v.type}})
  {{/for}})
{{/if}}
```

**4. Guard `in` lists with `if` too**, to avoid `in ()`:

```
{{if notIsEmpty($.request.body.ids)}}
  and id in ({{$.request.body.ids|join(",")}})
{{/if}}
```

**5. Limit the length of every array used by a loop or `join`.** Databases cap the number of parameters in one statement:

```
{{validate $.request.body.users rule="array|max:100" message="at most 100 users"}}
```

**6. Make the locating condition of an UPDATE / DELETE required with `validate`, and do not put it inside an `if`.** `where 1=1` is always valid SQL, so the engine cannot tell when every condition was skipped, and the statement would hit the whole table.

## Recipes

### Dynamic sorting and paging

```
{{validate $.request.body.page rule="int|min:1" message="page must be >= 1"}}
{{validate $.request.body.size rule="int|between:1,100" message="size must be between 1 and 100"}}
select id, name from users
where 1=1
order by {{$.request.body.sort|default("id")|ident("id,name,created_at")}} {{$.request.body.dir|default("desc")|lower|ident("asc,desc")}}
limit {{$.request.body.size|default(20)|int}} offset {{($.request.body.page|default(1)|int - 1) * ($.request.body.size|default(20)|int)}}
```

Column names and sort directions cannot be bound, so they must be restricted with `ident`. Paging syntax differs between databases and is written by the template author for the target dialect, for example on SQL Server:

```
order by id offset {{($.request.body.page|default(1)|int - 1) * 20}} rows fetch next {{20}} rows only
```

### IN query from one column of an array

```
{{if notIsEmpty($.request.body.users[*].user_id)}}
  and user_id in ({{$.request.body.users[*].user_id|join(",")}})
{{/if}}
```

### Batch insert

```
{{validate $.request.body.users rule="required|array|between:1,100" message="1 to 100 users required"}}
insert into user_tag (user_id, type) values
{{for u in $.request.body.users sep ","}}
  ({{$u.user_id}}, {{$u.type|default(1)}})
{{/for}}
```

### Update

```
{{validate $.request.body.id rule="required|int|min:1" message="id is required"}}
update users
set name = {{$.request.body.name}}, updated_at = {{"now()"|raw}}
where id = {{$.request.body.id}}
```

### Inside an HTTP handler

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

## Error handling

| Type | When | Examples |
| --- | --- | --- |
| `*drusql.ParseError` | During `Parse` | Unclosed tag, unknown function, non-literal `sep`, assignment to `$.request` |
| `drusql.ValidationErrors` | During `Exec` / `Validate` | A `validate` tag failed |
| `*drusql.ExecError` | During `Exec` | `undefined` reaching a value placeholder, an `ident` value outside the whitelist, `for` over a non-array, a failed type conversion |

`ParseError` and `ExecError` carry `Line`, `Col` and `Msg`, pointing at the position in the template. No error ever produces SQL.

```go
_, err := eng.Parse("select {{$.request.body.id|nosuch}}")
var pe *drusql.ParseError
if errors.As(err, &pe) {
	fmt.Println(pe.Line, pe.Col, pe.Msg) // 1 28 unknown function "nosuch"
}
```

## Notes

- Call `RegisterFunc` before `Parse`. A `Template` is immutable and can be executed from many goroutines at once.
- Whole numbers decoded from JSON (a `float64` with no fraction) are bound as `int64`.
- `{#` starts a comment and `{{` starts a tag. To emit either literally in SQL text, use the `{{"{{"|raw}}` form.
- The engine does not check SQL syntax and does not know whether a statement reads or writes; protecting UPDATE / DELETE relies on `validate`.
- Loading localized messages from a database (`loaddb`) is not supported yet.

## License

[Apache License 2.0](LICENSE)

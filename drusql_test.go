package drusql

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// squash 把连续空白压成一个空格，方便比较 SQL。
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test json: %v", err)
	}
	return m
}

// req 用 JSON 构造 {"request": {"body": ..., "header": ...}}。
func req(t *testing.T, body string) map[string]any {
	t.Helper()
	return Request(mustJSON(t, body), map[string]any{})
}

func run(t *testing.T, d Dialect, tpl string, data map[string]any) *Result {
	t.Helper()
	tp, err := New(d).Parse(tpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := tp.Exec(data)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	return res
}

func wantSQL(t *testing.T, res *Result, sql string, args ...any) {
	t.Helper()
	if got := squash(res.SQL); got != squash(sql) {
		t.Errorf("sql mismatch\n got: %s\nwant: %s", got, squash(sql))
	}
	if len(args) == 0 && len(res.Args) == 0 {
		return
	}
	if !reflect.DeepEqual(res.Args, args) {
		t.Errorf("args mismatch\n got: %#v\nwant: %#v", res.Args, args)
	}
}

func execErr(t *testing.T, tpl string, data map[string]any) error {
	t.Helper()
	tp, err := New(MySQL).Parse(tpl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = tp.Exec(data)
	if err == nil {
		t.Fatalf("expected an exec error for %q", tpl)
	}
	return err
}

func parseErr(t *testing.T, tpl string, want string) {
	t.Helper()
	_, err := New(MySQL).Parse(tpl)
	if err == nil {
		t.Fatalf("expected a parse error for %q", tpl)
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// ---- 规范第 11 节的完整示例 ----

const fullTemplate = `{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.too_many = "at most 100 users"}}
{{set $.lang.data.zh.too_many = "最多100个用户"}}
{{validate $.request.body.ids rule="array|max:100" message="最多100个id"}}
{{validate $.request.body.users rule="array|max:100" message=t("too_many")}}

select * from user_tag
where 1=1
{{if notIsEmpty($.request.body.name)}}
  and name like {{$.request.body.name|trim|like}} escape '!'
{{/if}}
{{if notIsEmpty($.request.body.ids)}}
  and id in ({{$.request.body.ids|join(",")}})
{{/if}}
{{if notIsEmpty($.request.body.users)}}
  and ({{for v in $.request.body.users sep "or"}}
    (user_id = {{$v.user_id}} and type = {{$v.type}})
  {{/for}})
{{/if}}
order by {{$.request.body.sort|default("id")|ident("id,created_at")}} desc
`

const fullRequest = `{
  "header": { "lang": "zh" },
  "body": {
    "name": " Tom ",
    "ids": [1, 2, 3],
    "users": [ { "user_id": 7, "type": 1 }, { "user_id": 8, "type": 2 } ],
    "sort": "created_at"
  }
}`

func TestFullExampleAllDialects(t *testing.T) {
	data := map[string]any{"request": mustJSON(t, fullRequest)}
	args := []any{"%Tom%", int64(1), int64(2), int64(3), int64(7), int64(1), int64(8), int64(2)}
	cases := map[Dialect]string{
		MySQL: `select * from user_tag where 1=1 and name like ? escape '!' and id in (?,?,?)
			and ((user_id = ? and type = ?) or (user_id = ? and type = ?)) order by created_at desc`,
		PostgreSQL: `select * from user_tag where 1=1 and name like $1 escape '!' and id in ($2,$3,$4)
			and ((user_id = $5 and type = $6) or (user_id = $7 and type = $8)) order by created_at desc`,
		Oracle: `select * from user_tag where 1=1 and name like :1 escape '!' and id in (:2,:3,:4)
			and ((user_id = :5 and type = :6) or (user_id = :7 and type = :8)) order by created_at desc`,
		SQLServer: `select * from user_tag where 1=1 and name like @p1 escape '!' and id in (@p2,@p3,@p4)
			and ((user_id = @p5 and type = @p6) or (user_id = @p7 and type = @p8)) order by created_at desc`,
	}
	for d, sql := range cases {
		t.Run(d.String(), func(t *testing.T) {
			wantSQL(t, run(t, d, fullTemplate, data), sql, args...)
		})
	}
}

func TestFullExampleOptionalFieldsMissing(t *testing.T) {
	res := run(t, MySQL, fullTemplate, req(t, `{}`))
	wantSQL(t, res, `select * from user_tag where 1=1 order by id desc`)
	if len(res.Args) != 0 {
		t.Errorf("expected no args, got %#v", res.Args)
	}
}

func TestFullExampleSortNotInWhitelist(t *testing.T) {
	err := execErr(t, fullTemplate, req(t, `{"sort": "name; drop table user"}`))
	var ee *ExecError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "whitelist") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFullExampleTooManyUsers(t *testing.T) {
	users := make([]any, 101)
	for i := range users {
		users[i] = map[string]any{"user_id": i, "type": 1}
	}
	data := Request(map[string]any{"users": users}, map[string]any{"lang": "zh"})
	tp := New(MySQL).MustParse(fullTemplate)
	_, err := tp.Exec(data)
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %v", err)
	}
	want := ValidationErrors{{Field: "$.request.body.users", Rule: "max", Message: "最多100个用户"}}
	if !reflect.DeepEqual(ve, want) {
		t.Errorf("got %#v", ve)
	}
}

// ---- 值占位符与绑定 ----

func TestValuePlaceholder(t *testing.T) {
	data := req(t, `{"name": "Tom", "age": 18, "score": 1.5, "ok": true, "nothing": null}`)
	res := run(t, MySQL, `a={{$.request.body.name}} b={{$.request.body.age}} c={{$.request.body.score}} d={{$.request.body.ok}} e={{$.request.body.nothing}}`, data)
	wantSQL(t, res, `a=? b=? c=? d=? e=?`, "Tom", int64(18), 1.5, true, nil)
}

func TestUndefinedPlaceholderIsError(t *testing.T) {
	err := execErr(t, `select {{$.request.body.missing}}`, req(t, `{}`))
	if !strings.Contains(err.Error(), "undefined") {
		t.Errorf("unexpected error: %v", err)
	}
	res := run(t, MySQL, `select {{$.request.body.missing|default("x")}}`, req(t, `{}`))
	wantSQL(t, res, `select ?`, "x")
}

func TestArrayPlaceholderNeedsJoin(t *testing.T) {
	err := execErr(t, `in ({{$.request.body.ids}})`, req(t, `{"ids": [1,2]}`))
	if !strings.Contains(err.Error(), "join") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPipeEqualsCall(t *testing.T) {
	data := req(t, `{"name": "  Tom  ", "users": [1,2,3]}`)
	a := run(t, MySQL, `{{$.request.body.name|trim|lower}} {{$.request.body.users|length}} {{$.request.body.x|default("d")}}`, data)
	b := run(t, MySQL, `{{lower(trim($.request.body.name))}} {{length($.request.body.users)}} {{default($.request.body.x, "d")}}`, data)
	if a.SQL != b.SQL || !reflect.DeepEqual(a.Args, b.Args) {
		t.Errorf("pipe and call differ: %#v vs %#v", a, b)
	}
	wantSQL(t, a, `? ? ?`, "tom", int64(3), "d")
}

func TestTrimThenDefaultOnMissingField(t *testing.T) {
	res := run(t, MySQL, `{{$.request.body.name|trim|default("")}}`, req(t, `{}`))
	wantSQL(t, res, `?`, "")
}

func TestTypeConversions(t *testing.T) {
	data := req(t, `{"a": "42", "b": "1.5", "c": 7, "d": "true", "e": "2026-11-17 03:04:05"}`)
	res := run(t, MySQL, `{{$.request.body.a|int}} {{$.request.body.b|float}} {{$.request.body.c|string}} {{$.request.body.d|bool}} {{$.request.body.e|date("Y-m-d")}} {{$.request.body.e|date("Y/m/d H:i:s")}}`, data)
	wantSQL(t, res, `? ? ? ? ? ?`, int64(42), 1.5, "7", true, "2026-11-17", "2026/11/17 03:04:05")

	err := execErr(t, `{{$.request.body.a|int}}`, req(t, `{"a": "abc"}`))
	if !strings.Contains(err.Error(), "cannot convert") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLike(t *testing.T) {
	data := req(t, `{"kw": "50%_off![x]"}`)
	res := run(t, MySQL, `{{$.request.body.kw|like}} {{$.request.body.kw|likeLeft}} {{$.request.body.kw|likeRight}}`, data)
	wantSQL(t, res, `? ? ?`, "%50!%!_off!![x]%", "%50!%!_off!![x]", "50!%!_off!![x]%")
	// SQL Server 还要转义 [
	res = run(t, SQLServer, `{{$.request.body.kw|like}}`, data)
	wantSQL(t, res, `@p1`, "%50!%!_off!!![x]%")
}

func TestRawAndLiteralBraces(t *testing.T) {
	res := run(t, MySQL, `select {{"now()"|raw}}, '{{"{{"|raw}}' from t`, req(t, `{}`))
	wantSQL(t, res, `select now(), '{{' from t`)
	if len(res.Args) != 0 {
		t.Errorf("raw must not bind: %#v", res.Args)
	}
}

func TestJSONNumbersBecomeIntegers(t *testing.T) {
	// encoding/json 把数字解成 float64，绑定前应还原为整数
	res := run(t, MySQL, `{{$.request.body.id}} {{$.request.body.price}}`, req(t, `{"id": 7, "price": 9.9}`))
	wantSQL(t, res, `? ?`, int64(7), 9.9)
}

// ---- 表达式 ----

func TestOperators(t *testing.T) {
	data := req(t, `{"id": 5, "name": "Tom", "users": [1,2], "empty": "", "sid": "5"}`)
	cases := []struct {
		cond string
		want bool
	}{
		{`$.request.body.id > 0`, true},
		{`$.request.body.id > 0 && $.request.body.name != ""`, true},
		{`$.request.body.id > 0 and $.request.body.name != ""`, true},
		{`$.request.body.id > 9 or $.request.body.name == "Tom"`, true},
		{`$.request.body.id > 9 || $.request.body.name == "Tom"`, true},
		{`not ($.request.body.id > 9)`, true},
		{`!($.request.body.id > 9)`, true},
		{`$.request.body.id >= 5 and $.request.body.id <= 5`, true},
		{`$.request.body.id < 5`, false},
		{`$.request.body.users|length > 1`, true},
		{`length($.request.body.users) == 2`, true},
		{`$.request.body.id + 1 == 6`, true},
		{`$.request.body.id * 2 - 4 / 2 == 8`, true},
		{`$.request.body.id % 2 == 1`, true},
		{`"a" ~ "b" ~ 1 == "ab1"`, true},
		{`$.request.body.sid == 5`, true},
		{`$.request.body.sid > 4`, true},
		{`true and false or true`, true},
		{`false and (false or true)`, false},
		{`-$.request.body.id == -5`, true},
		{`[1, 2]|length == 2`, true},
		{`{"a": 1, b: 2}|length == 2`, true},
		{`null == null`, true},
		{`$.request.body.name == null`, false},
	}
	for _, c := range cases {
		res := run(t, MySQL, `{{if `+c.cond+`}}yes{{else}}no{{/if}}`, data)
		want := "no"
		if c.want {
			want = "yes"
		}
		if res.SQL != want {
			t.Errorf("%s => %s, want %s", c.cond, res.SQL, want)
		}
	}
}

func TestUndefinedComparisonsAreFalse(t *testing.T) {
	data := req(t, `{}`)
	for _, cond := range []string{
		`$.request.body.id > 0`, `$.request.body.id == 0`, `$.request.body.name != ""`,
		`$.request.body.id < 0`, `$.request.body.flag`,
	} {
		res := run(t, MySQL, `{{if `+cond+`}}yes{{else}}no{{/if}}`, data)
		if res.SQL != "no" {
			t.Errorf("%s should be false when the field is missing", cond)
		}
	}
}

func TestIsFunctions(t *testing.T) {
	// 规范第 5 节的表
	data := req(t, `{"null": null, "str": "", "arr": [], "zero": 0, "szero": "0", "f": false, "v": "x"}`)
	cases := []struct {
		path                       string
		defined, isNull, isemptyOK bool
	}{
		{"missing", false, false, true},
		{"null", true, true, true},
		{"str", true, false, true},
		{"arr", true, false, true},
		{"zero", true, false, false},
		{"szero", true, false, false},
		{"f", true, false, false},
		{"v", true, false, false},
	}
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	for _, c := range cases {
		p := "$.request.body." + c.path
		tpl := `{{if isDefined(` + p + `)}}1{{else}}0{{/if}}{{if isNull(` + p + `)}}1{{else}}0{{/if}}{{if isEmpty(` + p + `)}}1{{else}}0{{/if}}`
		res := run(t, MySQL, tpl, data)
		want := b(c.defined) + b(c.isNull) + b(c.isemptyOK)
		if res.SQL != want {
			t.Errorf("%s: got %s want %s", c.path, res.SQL, want)
		}
	}
}

func TestIfConditionMustBeBoolean(t *testing.T) {
	err := execErr(t, `{{if $.request.body.id}}x{{/if}}`, req(t, `{"id": 5}`))
	if !strings.Contains(err.Error(), "boolean") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---- 条件 ----

func TestIfElseifElse(t *testing.T) {
	tpl := `select * from orders
where 1=1
{{if isDefined($.request.body.id) and $.request.body.id > 0}}
  and id = {{$.request.body.id}}
{{elseif notIsEmpty($.request.body.user_name)}}
  and user_name = {{$.request.body.user_name}}
{{else}}
  and status = 1
{{/if}}`
	wantSQL(t, run(t, MySQL, tpl, req(t, `{"id": 3, "user_name": "a"}`)), `select * from orders where 1=1 and id = ?`, int64(3))
	wantSQL(t, run(t, MySQL, tpl, req(t, `{"user_name": "a"}`)), `select * from orders where 1=1 and user_name = ?`, "a")
	wantSQL(t, run(t, MySQL, tpl, req(t, `{}`)), `select * from orders where 1=1 and status = 1`)
}

func TestSkippedBranchIsNotEvaluated(t *testing.T) {
	// 未命中分支里的 undefined 不报错
	res := run(t, MySQL, `where 1=1 {{if isDefined($.request.body.id)}} and id = {{$.request.body.id}} {{/if}}`, req(t, `{}`))
	wantSQL(t, res, `where 1=1`)
}

func TestWritingConventions(t *testing.T) {
	tpl := `where 1=1
and (1=0
  {{if notIsEmpty($.request.body.id)}} or id = {{$.request.body.id}} {{/if}}
  {{if notIsEmpty($.request.body.id2)}} or id = {{$.request.body.id2}} {{/if}}
)`
	wantSQL(t, run(t, MySQL, tpl, req(t, `{"id2": 9}`)), `where 1=1 and (1=0 or id = ? )`, int64(9))
	wantSQL(t, run(t, MySQL, tpl, req(t, `{}`)), `where 1=1 and (1=0 )`)
}

// ---- 循环 ----

func TestForSep(t *testing.T) {
	tpl := `{{for v in $.request.body.users sep "or"}}
   (user_id = {{$v.user_id}} and type = {{$v.type}})
{{/for}}`
	data := req(t, `{"users": [{"user_id": 1, "type": 1}, {"user_id": 2, "type": 2}, {"user_id": 3, "type": 3}]}`)
	res := run(t, MySQL, tpl, data)
	want := `(user_id = ? and type = ?) or (user_id = ? and type = ?) or (user_id = ? and type = ?)`
	if res.SQL != want {
		t.Errorf("got %q\nwant %q", res.SQL, want)
	}
	if !reflect.DeepEqual(res.Args, []any{int64(1), int64(1), int64(2), int64(2), int64(3), int64(3)}) {
		t.Errorf("args: %#v", res.Args)
	}
}

func TestForInlineAndEmpty(t *testing.T) {
	res := run(t, MySQL, `({{for v in $.request.body.ids sep "or"}}id = {{$v}}{{/for}})`, req(t, `{"ids": [1, 2]}`))
	wantSQL(t, res, `(id = ? or id = ?)`, int64(1), int64(2))

	res = run(t, MySQL, `({{for v in $.request.body.ids sep "or"}}id = {{$v}}{{/for}})`, req(t, `{"ids": []}`))
	wantSQL(t, res, `()`)

	res = run(t, MySQL, `{{for v in $.request.body.ids}}{{$v}}{{/for}}`, req(t, `{"ids": [1, 2]}`))
	wantSQL(t, res, `? ?`, int64(1), int64(2))
}

func TestForBatchInsert(t *testing.T) {
	tpl := `insert into user_tag (user_id, type) values
{{for v in $.request.body.users sep ","}}
  ({{$v.user_id}}, {{$v.type}})
{{/for}}`
	res := run(t, PostgreSQL, tpl, req(t, `{"users": [{"user_id": 1, "type": 1}, {"user_id": 2, "type": 2}]}`))
	wantSQL(t, res, `insert into user_tag (user_id, type) values ($1, $2) , ($3, $4)`, int64(1), int64(1), int64(2), int64(2))
}

func TestForNested(t *testing.T) {
	tpl := `{{for g in $.request.body.groups sep "or"}}({{for id in $g.ids sep "and"}}{{if $id > 1}}a = {{$id}}{{else}}b = {{$id}}{{/if}}{{/for}}){{/for}}`
	res := run(t, MySQL, tpl, req(t, `{"groups": [{"ids": [1, 2]}, {"ids": [3]}]}`))
	wantSQL(t, res, `(b = ? and a = ?) or (a = ?)`, int64(1), int64(2), int64(3))
}

func TestForNotArray(t *testing.T) {
	err := execErr(t, `{{for v in $.request.body.users}}x{{/for}}`, req(t, `{}`))
	if !strings.Contains(err.Error(), "expects an array") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoopVariableNotVisibleOutside(t *testing.T) {
	parseErr(t, `{{for v in $.request.body.a}}x{{/for}}{{$v}}`, "unknown variable $v")
}

// ---- JSONPath ----

func TestJSONPath(t *testing.T) {
	data := req(t, `{
		"users": [
			{"user_id": 7, "type": 1, "name": "a"},
			{"user_id": 8, "type": 2, "name": "b"},
			{"user_id": 9, "type": 1, "name": "c"}
		],
		"ids": [10, 20, 30, 40, 50],
		"X-Key": "k",
		"deep": {"a": {"user_id": 1}, "b": [{"user_id": 2}]}
	}`)
	cases := []struct {
		tpl  string
		args []any
	}{
		{`{{$.request.body.users[0].user_id}}`, []any{int64(7)}},
		{`{{$.request.body.users[-1].name}}`, []any{"c"}},
		{`{{$.request.body['X-Key']}}`, []any{"k"}},
		{`{{$.request.body["users"][1]["name"]}}`, []any{"b"}},
		{`{{$.request.body.users[*].user_id|join(",")}}`, []any{int64(7), int64(8), int64(9)}},
		{`{{$.request.body.ids[0:2]|join(",")}}`, []any{int64(10), int64(20)}},
		{`{{$.request.body.ids[-2:]|join(",")}}`, []any{int64(40), int64(50)}},
		{`{{$.request.body.ids[::2]|join(",")}}`, []any{int64(10), int64(30), int64(50)}},
		{`{{$.request.body.ids[0,4]|join(",")}}`, []any{int64(10), int64(50)}},
		{`{{$.request.body.users[?@.type == 1].user_id|join(",")}}`, []any{int64(7), int64(9)}},
		{`{{$.request.body.users[?@.type == 1 && @.user_id > 7].name|join(",")}}`, []any{"c"}},
		{`{{$.request.body.users[?@.type != 1 || @.name == 'a'].name|join(",")}}`, []any{"a", "b"}},
		{`{{$.request.body.users[?!(@.type == 1)].name|join(",")}}`, []any{"b"}},
		{`{{$.request.body.users[?@.missing].name|length}}`, []any{int64(0)}},
		{`{{$.request.body.ids[?@ >= 40]|join(",")}}`, []any{int64(40), int64(50)}},
		{`{{$.request.body.deep..user_id|join(",")}}`, []any{int64(1), int64(2)}},
		{`{{$.request.body.users[9].name|default("none")}}`, []any{"none"}},
	}
	for _, c := range cases {
		res := run(t, MySQL, c.tpl, data)
		if !reflect.DeepEqual(res.Args, c.args) {
			t.Errorf("%s\n got %#v\nwant %#v", c.tpl, res.Args, c.args)
		}
	}
}

func TestWildcardAlwaysReturnsArray(t *testing.T) {
	res := run(t, MySQL, `{{if isEmpty($.request.body.users[*].id)}}empty{{else}}has{{/if}}`, req(t, `{}`))
	if res.SQL != "empty" {
		t.Errorf("got %q", res.SQL)
	}
}

func TestLoopVariablePath(t *testing.T) {
	tpl := `{{for u in $.request.body.users sep ","}}{{$u.tags[0]}}{{/for}}`
	res := run(t, MySQL, tpl, req(t, `{"users": [{"tags": ["a", "b"]}, {"tags": ["c"]}]}`))
	wantSQL(t, res, `? , ?`, "a", "c")
}

// ---- set 与多语言 ----

func TestSetAndT(t *testing.T) {
	tpl := `{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.name = "name not found"}}
{{set $.lang.data.zh.name = "名称不存在"}}
{{set $.lang.data.en.only_en = "english only"}}
select {{t("name")}}, {{t("only_en")}}, {{t("no_such_key")}}`
	zh := Request(map[string]any{}, map[string]any{"lang": "zh"})
	wantSQL(t, run(t, MySQL, tpl, zh), `select ?, ?, ?`, "名称不存在", "english only", "no_such_key")
	none := Request(map[string]any{}, map[string]any{})
	wantSQL(t, run(t, MySQL, tpl, none), `select ?, ?, ?`, "name not found", "english only", "no_such_key")
}

func TestSetInsideLoopIsScoped(t *testing.T) {
	tpl := `{{set $.x = "outer"}}{{for v in $.request.body.a}}{{set $.x = $v}}{{$.x}}{{/for}} {{$.x}}`
	res := run(t, MySQL, tpl, req(t, `{"a": ["in1", "in2"]}`))
	wantSQL(t, res, `? ? ?`, "in1", "in2", "outer")
}

func TestSetObjectLiteral(t *testing.T) {
	res := run(t, MySQL, `{{set $.cfg = {"a": {"b": [1, 2]}}}}{{$.cfg.a.b[1]}}`, req(t, `{}`))
	wantSQL(t, res, `?`, int64(2))
}

func TestInputIsNotMutated(t *testing.T) {
	data := map[string]any{"request": map[string]any{"body": map[string]any{}}, "lang": map[string]any{"current": "zh"}}
	run(t, MySQL, `{{set $.lang.current = "en"}}{{set $.other = 1}}x`, data)
	if data["lang"].(map[string]any)["current"] != "zh" {
		t.Errorf("input was mutated: %#v", data)
	}
	if _, ok := data["other"]; ok {
		t.Errorf("input was mutated: %#v", data)
	}
}

// ---- 加载期错误 ----

func TestParseErrors(t *testing.T) {
	parseErr(t, `select {{$.request.body.id`, "unclosed tag")
	parseErr(t, `{{if true}}x`, "unclosed {{if}}")
	parseErr(t, `x{{/if}}`, "unexpected {{/if}}")
	parseErr(t, `{{for v in $.request.body.a}}x{{/if}}`, "unexpected {{/if}} inside {{for}}")
	parseErr(t, `{{if true}}a{{else true}}b{{/if}}`, "takes no condition")
	parseErr(t, `{{$.request.body.id|nosuch}}`, `unknown function "nosuch"`)
	parseErr(t, `{{nosuch($.request.body.id)}}`, `unknown function "nosuch"`)
	parseErr(t, `{{$.request.body.id|if}}`, "reserved word")
	parseErr(t, `{{set(1)}}`, "invalid set tag")
	parseErr(t, `{{for v in $.request.body.a sep $.request.body.s}}x{{/for}}`, "sep must be a string literal")
	parseErr(t, `{{$.request.body.ids|join(",")|trim}}`, "join must be the last function")
	parseErr(t, `{{if $.request.body.ids|join(",")}}x{{/if}}`, "join must be the last function")
	parseErr(t, `{{$.request.body.ids|join($.request.body.sep)}}`, "must be a string literal")
	parseErr(t, `{{$.request.body.sort|ident($.request.body.list)}}`, "must be a string literal")
	parseErr(t, `{{set $.request.body.id = 1}}`, "read-only")
	parseErr(t, `{{set $.a[0] = 1}}`, "member access only")
	parseErr(t, `{{if true}}{{validate $.request.body.id rule="required"}}{{/if}}`, "top level")
	parseErr(t, `{{validate $.request.body.id rule="nosuch"}}`, "unknown validation rule")
	parseErr(t, `{{validate $.request.body.id rule="max"}}`, "expects 1 argument")
	parseErr(t, `{{validate $.request.body.id message="x"}}`, "requires a rule")
	parseErr(t, `{{validate $.request.body..id rule="required"}}`, "members, indexes and [*] only")
	parseErr(t, `{{$.request.body.}}`, "expected a name after '.'")
	parseErr(t, `{{$.request.body.a[?@.x ==]}}`, "invalid path")
	parseErr(t, `{{$.request.body.id +}}`, "unexpected end of expression")
	parseErr(t, `{{trim()}}`, "wrong number of arguments")
	parseErr(t, `{{name}}`, "variables start with $")
	parseErr(t, `{# never closed`, "unclosed comment")
	parseErr(t, `{{}}`, "empty tag")
}

func TestParseErrorPosition(t *testing.T) {
	_, err := New(MySQL).Parse("select 1\nfrom t\nwhere id = {{$.request.body.id|nosuch}}")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %v", err)
	}
	if pe.Line != 3 || pe.Col != 32 {
		t.Errorf("position: line %d col %d", pe.Line, pe.Col)
	}
}

// ---- 模板排版 ----

func TestStandaloneTagsLeaveNoBlankLines(t *testing.T) {
	tpl := "{{set $.a = 1}}\n{# 注释 #}\nselect *\nfrom t\nwhere 1=1\n  {{if true}}\n  and a = {{$.a}}\n  {{/if}}\n{{if false}}\n  and b = 2\n{{/if}}\norder by id\n"
	res := run(t, MySQL, tpl, req(t, `{}`))
	want := "select *\nfrom t\nwhere 1=1\n  and a = ?\norder by id\n"
	if res.SQL != want {
		t.Errorf("got %q\nwant %q", res.SQL, want)
	}
}

func TestCommentAndPlainBraces(t *testing.T) {
	res := run(t, MySQL, `select '{"a": 1}' {# 说明 #}from t where j = '{}'`, req(t, `{}`))
	wantSQL(t, res, `select '{"a": 1}' from t where j = '{}'`)
}

func TestStringWithClosingBraces(t *testing.T) {
	res := run(t, MySQL, `{{"a}}b"}}`, req(t, `{}`))
	wantSQL(t, res, `?`, "a}}b")
}

// ---- 自定义函数、并发 ----

func TestRegisterFunc(t *testing.T) {
	e := New(MySQL)
	err := e.RegisterFunc("mask", 1, 1, func(args []any) (any, error) {
		s, _ := args[0].(string)
		if len(s) <= 2 {
			return s, nil
		}
		return s[:2] + "***", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.MustParse(`{{$.request.body.phone|mask}} {{mask("ab")}}`).Exec(req(t, `{"phone": "13800000000"}`))
	if err != nil {
		t.Fatal(err)
	}
	wantSQL(t, res, `? ?`, "13***", "ab")

	if err := e.RegisterFunc("if", 1, 1, func([]any) (any, error) { return nil, nil }); err == nil {
		t.Error("reserved word must be rejected")
	}
	if err := e.RegisterFunc("trim", 1, 1, func([]any) (any, error) { return nil, nil }); err == nil {
		t.Error("built-in name must be rejected")
	}
}

func TestConcurrentExec(t *testing.T) {
	tp := New(PostgreSQL).MustParse(fullTemplate)
	data := map[string]any{"request": mustJSON(t, fullRequest)}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := tp.Exec(data)
			if err != nil || len(res.Args) != 8 {
				t.Errorf("concurrent exec failed: %v", err)
			}
		}()
	}
	wg.Wait()
}

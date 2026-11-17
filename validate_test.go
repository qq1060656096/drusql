package drusql

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// 规范第 8 节的完整示例：4 处错误一次全部返回。
func TestValidationFullExample(t *testing.T) {
	tpl := `{{set $.lang.current = $.request.header.lang|default("en")}}
{{set $.lang.data.en.name_required = "name is required"}}
{{set $.lang.data.zh.name_required = "请填写名称"}}

{{validate $.request.body.name rule="required" message=t("name_required")}}
{{validate $.request.body.name rule="string|max:100" message="名称最多100个字符"}}
{{validate $.request.body.users rule="required|array|max:100" message="用户列表必填，最多100个"}}
{{validate $.request.body.users[*].user_id rule="required|int" message="用户 id 必填且为整数"}}
{{validate $.request.body.users[*].type rule="in:1,2,3" message="用户类型不合法"}}

insert into user_tag (name, user_id, type) values
{{for v in $.request.body.users sep ","}}
  ({{$.request.body.name}}, {{$v.user_id}}, {{$v.type}})
{{/for}}`
	data := map[string]any{"request": mustJSON(t, `{
		"header": { "lang": "zh" },
		"body": {
			"name": "",
			"users": [
				{ "user_id": 7, "type": 1 },
				{ "type": 9 },
				{ "user_id": "abc", "type": 2 }
			]
		}
	}`)}
	tp := New(MySQL).MustParse(tpl)
	res, err := tp.Exec(data)
	if res != nil {
		t.Fatalf("SQL must not be rendered when validation fails")
	}
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
	}
	want := ValidationErrors{
		{Field: "$.request.body.name", Rule: "required", Message: "请填写名称"},
		{Field: "$.request.body.users[1].user_id", Rule: "required", Message: "用户 id 必填且为整数"},
		{Field: "$.request.body.users[2].user_id", Rule: "int", Message: "用户 id 必填且为整数"},
		{Field: "$.request.body.users[1].type", Rule: "in", Message: "用户类型不合法"},
	}
	if !reflect.DeepEqual(ve, want) {
		t.Errorf("got  %#v\nwant %#v", ve, want)
	}

	// 返回给前端的 JSON 形状
	out, _ := json.Marshal(map[string]any{"errors": ve[:1]})
	if string(out) != `{"errors":[{"field":"$.request.body.name","rule":"required","message":"请填写名称"}]}` {
		t.Errorf("json: %s", out)
	}

	// Validate 只做校验，结果相同
	only, err := tp.Validate(data)
	if err != nil || !reflect.DeepEqual(only, want) {
		t.Errorf("Validate: %#v, %v", only, err)
	}

	// 数据合法时正常渲染
	ok := map[string]any{"request": mustJSON(t, `{"body": {"name": "n", "users": [{"user_id": 7, "type": 1}, {"user_id": "8", "type": 2}]}}`)}
	res, err = tp.Exec(ok)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	wantSQL(t, res, `insert into user_tag (name, user_id, type) values (?, ?, ?) , (?, ?, ?)`,
		"n", int64(7), int64(1), "n", "8", int64(2))
}

func TestValidationWildcardOnMissingArray(t *testing.T) {
	// 父数组不存在时 [*] 不产生任何校验项，由父字段自己的 validate 负责
	tp := New(MySQL).MustParse(`{{validate $.request.body.users[*].id rule="required"}}x`)
	if _, err := tp.Exec(req(t, `{}`)); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidationNestedWildcardAndIndex(t *testing.T) {
	tp := New(MySQL).MustParse(`{{validate $.request.body.rows[*].tags[*] rule="string|max:3" message="bad tag"}}
{{validate $.request.body.rows[0].id rule="required|int|min:1" message="bad id"}}x`)
	ve, err := tp.Validate(req(t, `{"rows": [{"id": 0, "tags": ["ok", "toolong"]}, {"tags": [1]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := ValidationErrors{
		{Field: "$.request.body.rows[0].tags[1]", Rule: "max", Message: "bad tag"},
		{Field: "$.request.body.rows[1].tags[0]", Rule: "string", Message: "bad tag"},
		{Field: "$.request.body.rows[0].id", Rule: "min", Message: "bad id"},
	}
	if !reflect.DeepEqual(ve, want) {
		t.Errorf("got %#v", ve)
	}
}

func TestValidationDefaultMessage(t *testing.T) {
	tp := New(MySQL).MustParse(`{{validate $.request.body.id rule="required"}}x`)
	ve, _ := tp.Validate(req(t, `{}`))
	if len(ve) != 1 || ve[0].Message != "$.request.body.id failed the required rule" {
		t.Errorf("got %#v", ve)
	}
}

func TestRules(t *testing.T) {
	cases := []struct {
		rule string
		json string // body.v 的值；"-" 表示字段不存在
		fail string // 期望失败的规则名，"" 表示通过
	}{
		// required 与跳过规则
		{"required", "-", "required"},
		{"required", `null`, "required"},
		{"required", `""`, "required"},
		{"required", `[]`, "required"},
		{"required", `0`, ""},
		{"required", `false`, ""},
		{"string|max:3", "-", ""},
		{"string|max:3", `null`, ""},
		{"string|max:3", `""`, ""},
		// 类型
		{"string", `"a"`, ""},
		{"string", `1`, "string"},
		{"int", `5`, ""},
		{"int", `"5"`, ""},
		{"int", `"-5"`, ""},
		{"int", `5.5`, "int"},
		{"int", `"abc"`, "int"},
		{"integer", `"abc"`, "int"},
		{"numeric", `"1.5"`, ""},
		{"numeric", `"1.5x"`, "numeric"},
		{"bool", `true`, ""},
		{"bool", `1`, ""},
		{"bool", `"0"`, ""},
		{"bool", `"yes"`, "bool"},
		{"boolean", `2`, "bool"},
		{"array", `[1]`, ""},
		{"array", `{"a": 1}`, "array"},
		{"date", `"2026-11-17"`, ""},
		{"date", `"2026-11-17 03:04:05"`, ""},
		{"date", `"not a date"`, "date"},
		// 大小：字符串按字符数、数字按大小、数组按元素个数
		{"string|max:3", `"abc"`, ""},
		{"string|max:3", `"abcd"`, "max"},
		{"string|max:2", `"中文字"`, "max"},
		{"string|min:2", `"a"`, "min"},
		{"int|min:1", `0`, "min"},
		{"int|max:100", `101`, "max"},
		{"int|max:100", `"101"`, "max"},
		{"max:100", `"101"`, ""},
		{"array|max:2", `[1,2,3]`, "max"},
		{"array|min:1", `[]`, "min"},
		{"int|between:1,10", `10`, ""},
		{"int|between:1,10", `11`, "between"},
		// in 与 regex
		{"in:1,2,3", `2`, ""},
		{"in:1,2,3", `"2"`, ""},
		{"in:a, b", `"b"`, ""},
		{"in:1,2,3", `9`, "in"},
		{"regex:^[a-z]+$", `"abc"`, ""},
		{"regex:^[a-z]+$", `"abc1"`, "regex"},
		{"regex:/^(a|b)$/", `"b"`, ""},
		{"string|regex:^(a|b)$", `"c"`, "regex"},
		// 第一条失败即为结果
		{"required|string|max:3", `12345`, "string"},
	}
	for _, c := range cases {
		tp, err := New(MySQL).Parse(`{{validate $.request.body.v rule="` + c.rule + `" message="m"}}x`)
		if err != nil {
			t.Errorf("%s: parse: %v", c.rule, err)
			continue
		}
		body := `{}`
		if c.json != "-" {
			body = `{"v": ` + c.json + `}`
		}
		ve, err := tp.Validate(req(t, body))
		if err != nil {
			t.Errorf("%s on %s: %v", c.rule, c.json, err)
			continue
		}
		got := ""
		if len(ve) > 0 {
			got = ve[0].Rule
		}
		if got != c.fail {
			t.Errorf("rule %q on %s: failed %q, want %q", c.rule, c.json, got, c.fail)
		}
	}
}

func TestValidateRunsBeforeRenderRegardlessOfPosition(t *testing.T) {
	// validate 写在 SQL 后面，也先于渲染生效：这里不会因为 id 是 undefined 报 ExecError
	tp := New(MySQL).MustParse(`select * from t where id = {{$.request.body.id}}
{{validate $.request.body.id rule="required" message="id 必填"}}`)
	_, err := tp.Exec(req(t, `{}`))
	var ve ValidationErrors
	if !errors.As(err, &ve) || len(ve) != 1 || ve[0].Message != "id 必填" {
		t.Errorf("expected the validation error first, got %v", err)
	}
}

package drusql_test

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/qq1060656096/drusql"
)

func Example() {
	eng := drusql.New(drusql.PostgreSQL)
	tpl := eng.MustParse(`
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

	var body map[string]any
	_ = json.Unmarshal([]byte(`{"name": " Tom ", "ids": [1, 2, 3]}`), &body)

	res, err := tpl.Exec(drusql.Request(body, nil))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.SQL)
	fmt.Println(res.Args...)
	// Output:
	// select * from users
	// where 1=1
	//   and name like $1 escape '!'
	//   and id in ($2,$3,$4)
	// order by id desc
	// %Tom% 1 2 3
}

func Example_validation() {
	tpl := drusql.New(drusql.MySQL).MustParse(`
{{validate $.request.body.name rule="required|string|max:100" message="请填写名称"}}
{{validate $.request.body.users[*].type rule="in:1,2,3" message="用户类型不合法"}}
insert into t (name) values ({{$.request.body.name}})`)

	body := map[string]any{
		"name":  "",
		"users": []any{map[string]any{"type": 1}, map[string]any{"type": 9}},
	}
	_, err := tpl.Exec(drusql.Request(body, nil))

	var verrs drusql.ValidationErrors
	if errors.As(err, &verrs) {
		out, _ := json.MarshalIndent(map[string]any{"errors": verrs}, "", "  ")
		fmt.Println(string(out))
	}
	// Output:
	// {
	//   "errors": [
	//     {
	//       "field": "$.request.body.name",
	//       "rule": "required",
	//       "message": "请填写名称"
	//     },
	//     {
	//       "field": "$.request.body.users[1].type",
	//       "rule": "in",
	//       "message": "用户类型不合法"
	//     }
	//   ]
	// }
}

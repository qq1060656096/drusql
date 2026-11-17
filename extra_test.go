package drusql

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type testUser struct {
	ID   int      `json:"user_id"`
	Tags []string `json:"tags"`
	Note *string  `json:"note"`
}

func TestGoNativeInputs(t *testing.T) {
	note := "hi"
	body := map[string]any{
		"i8":     int8(8),
		"u32":    uint32(32),
		"f32":    float32(1.5),
		"num":    json.Number("12"),
		"numf":   json.Number("1.25"),
		"ints":   []int{1, 2},
		"strs":   []string{"a", "b"},
		"smap":   map[string]string{"k": "v"},
		"user":   testUser{ID: 7, Tags: []string{"x"}, Note: &note},
		"users":  []testUser{{ID: 1}, {ID: 2}},
		"ptr":    &note,
		"nilptr": (*string)(nil),
		"when":   time.Date(2026, 11, 17, 3, 4, 5, 0, time.UTC),
	}
	tpl := `{{$.request.body.i8}} {{$.request.body.u32}} {{$.request.body.f32}} {{$.request.body.num}} {{$.request.body.numf}}
{{$.request.body.ints|join(",")}} {{$.request.body.strs[1]}} {{$.request.body.smap.k}}
{{$.request.body.user.user_id}} {{$.request.body.user.tags[0]}} {{$.request.body.user.note}}
{{$.request.body.users[*].user_id|join(",")}} {{$.request.body.ptr}} {{$.request.body.nilptr}}
{{$.request.body.when|date("Y-m-d H:i:s")}}`
	res := run(t, MySQL, tpl, Request(body, nil))
	want := []any{int64(8), int64(32), 1.5, int64(12), 1.25,
		int64(1), int64(2), "b", "v",
		int64(7), "x", "hi",
		int64(1), int64(2), "hi", nil,
		"2026-11-17 03:04:05"}
	if !reflect.DeepEqual(res.Args, want) {
		t.Errorf("got  %#v\nwant %#v", res.Args, want)
	}
}

func TestFilterLiteralsAndSlices(t *testing.T) {
	data := req(t, `{"rows": [
		{"n": "a", "on": true, "v": null, "p": 1.5},
		{"n": "b", "on": false, "v": 1, "p": 2.5},
		{"n": "c", "on": true, "p": -1}
	], "ids": [1, 2, 3, 4, 5], "limit": 2}`)
	cases := []struct {
		tpl  string
		args []any
	}{
		{`{{$.request.body.rows[?@.on == true].n|join(",")}}`, []any{"a", "c"}},
		{`{{$.request.body.rows[?@.v == null].n|join(",")}}`, []any{"a"}},
		{`{{$.request.body.rows[?@.n == "b" || @.n == 'c'].n|join(",")}}`, []any{"b", "c"}},
		{`{{$.request.body.rows[?@.p > 1.5].n|join(",")}}`, []any{"b"}},
		{`{{$.request.body.rows[?@.p <= -1].n|join(",")}}`, []any{"c"}},
		{`{{$.request.body.rows[?@.v].n|join(",")}}`, []any{"a", "b"}},
		{`{{$.request.body.rows[?(@.on == true) && !@.v].n|join(",")}}`, []any{"c"}},
		{`{{$.request.body.ids[?@ > $.request.body.limit]|join(",")}}`, []any{int64(3), int64(4), int64(5)}},
		{`{{$.request.body.ids[::-1]|join(",")}}`, []any{int64(5), int64(4), int64(3), int64(2), int64(1)}},
		{`{{$.request.body.ids[3:0:-2]|join(",")}}`, []any{int64(4), int64(2)}},
		{`{{$.request.body.ids[1:-1]|join(",")}}`, []any{int64(2), int64(3), int64(4)}},
		{`{{$.request.body.ids[10:]|length}}`, []any{int64(0)}},
		{`{{$.request.body.rows.*.n|length}}`, []any{int64(3)}},
		{`{{$.request.body..n|join(",")}}`, []any{"a", "b", "c"}},
	}
	for _, c := range cases {
		res := run(t, MySQL, c.tpl, data)
		if !reflect.DeepEqual(res.Args, c.args) {
			t.Errorf("%s\n got %#v\nwant %#v", c.tpl, res.Args, c.args)
		}
	}
}

func TestConversionEdgeCases(t *testing.T) {
	data := req(t, `{"f": 3.9, "t": true, "s": "x", "n": null, "one": 1, "ts": 0}`)
	res := run(t, MySQL, `{{$.request.body.f|int}} {{$.request.body.t|int}} {{$.request.body.one|float}} {{$.request.body.one|bool}} {{$.request.body.t|string}} {{$.request.body.f|string}} {{$.request.body.n|int}} {{$.request.body.s|length}} {{$.request.body.missing|length}}`, data)
	wantSQL(t, res, `? ? ? ? ? ? ? ? ?`, int64(3), int64(1), float64(1), true, "true", "3.9", nil, int64(1), int64(0))

	for _, tpl := range []string{
		`{{$.request.body.s|float}}`, `{{$.request.body.s|bool}}`, `{{$.request.body.f|bool}}`,
		`{{$.request.body.s|date}}`, `{{$.request.body.one|length}}`, `{{$.request.body.one + "a"}}`,
		`{{$.request.body.one / 0}}`, `{{-$.request.body.s}}`, `{{$.request.body.missing|raw}}`,
		`{{$.request.body.missing|join(",")}}`, `{{$.request.body.missing|ident("a,b")}}`,
		`{{set $.x = $.request.body.s|raw}}`,
	} {
		execErr(t, tpl, data)
	}
}

func TestStringEscapes(t *testing.T) {
	res := run(t, MySQL, `{{"a\"b"}} {{'it\'s'}} {{"line\nbreak"}} {{"back\\slash"}}`, req(t, `{}`))
	wantSQL(t, res, `? ? ? ?`, `a"b`, `it's`, "line\nbreak", `back\slash`)
}

func TestMultiLineTagAndWhitespace(t *testing.T) {
	tpl := "{{ if $.request.body.id > 0\n      and $.request.body.id < 10 }}ok{{ else }}no{{ /if }}"
	res := run(t, MySQL, tpl, req(t, `{"id": 5}`))
	if res.SQL != "ok" {
		t.Errorf("got %q", res.SQL)
	}
}

func TestNullHandling(t *testing.T) {
	tpl := `where 1=1 {{if isNull($.request.body.deleted_at)}} and deleted_at is null {{else}} and deleted_at = {{$.request.body.deleted_at}} {{/if}}`
	wantSQL(t, run(t, MySQL, tpl, req(t, `{"deleted_at": null}`)), `where 1=1 and deleted_at is null`)
	wantSQL(t, run(t, MySQL, tpl, req(t, `{"deleted_at": "2026-01-01"}`)), `where 1=1 and deleted_at = ?`, "2026-01-01")
}

func TestUpdateWithRequiredGuard(t *testing.T) {
	// 规范第 10 节约定 6：写语句的定位条件必须 validate 必填
	tp := New(MySQL).MustParse(`{{validate $.request.body.id rule="required|int|min:1" message="id 必填"}}

update user set name = {{$.request.body.name}} where id = {{$.request.body.id}}`)
	if _, err := tp.Exec(req(t, `{"name": "n"}`)); err == nil {
		t.Fatal("update without id must be rejected")
	}
	res, err := tp.Exec(req(t, `{"name": "n", "id": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	wantSQL(t, res, `update user set name = ? where id = ?`, "n", int64(3))
}

func TestTopLevelSetRunsOnceInResult(t *testing.T) {
	res := run(t, MySQL, `{{set $.n = default($.n, 0) + 1}}{{$.n}}`, req(t, `{}`))
	wantSQL(t, res, `?`, int64(1))
}

func TestNotIsEmpty(t *testing.T) {
	data := req(t, `{"s": "", "arr": [], "n": null, "zero": 0, "v": "x"}`)
	for path, want := range map[string]string{"missing": "no", "s": "no", "arr": "no", "n": "no", "zero": "yes", "v": "yes"} {
		p := "$.request.body." + path
		a := run(t, MySQL, `{{if notIsEmpty(`+p+`)}}yes{{else}}no{{/if}}`, data)
		b := run(t, MySQL, `{{if `+p+`|notIsEmpty}}yes{{else}}no{{/if}}`, data)
		c := run(t, MySQL, `{{if not isEmpty(`+p+`)}}yes{{else}}no{{/if}}`, data)
		if a.SQL != want || b.SQL != want || c.SQL != want {
			t.Errorf("%s: got %s / %s / %s, want %s", path, a.SQL, b.SQL, c.SQL, want)
		}
	}
	// 旧的全小写名字不再可用
	parseErr(t, `{{if isempty($.request.body.v)}}x{{/if}}`, `unknown function "isempty"`)
	parseErr(t, `{{$.request.body.v|like_left}}`, `unknown function "like_left"`)
}

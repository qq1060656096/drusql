package drusql

import "testing"

func TestLangInOneSet(t *testing.T) {
	tpl := `{{set $.lang = {
  "current": $.request.header.lang|default("en"),
  "default": "en",
  "data": {
    "en": {"name_required": "name is required", "too_many": "at most 100"},
    "zh": {"name_required": "请填写名称"}
  }
}}}
select {{t("name_required")}}, {{t("too_many")}}`
	res := run(t, MySQL, tpl, Request(map[string]any{}, map[string]any{"lang": "zh"}))
	wantSQL(t, res, `select ?, ?`, "请填写名称", "at most 100")
	if res.SQL != "select ?, ?" {
		t.Errorf("got %q", res.SQL)
	}
}

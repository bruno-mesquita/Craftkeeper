package httpapi_test

import (
	"net/http"
	"strconv"
	"testing"
)

func TestAudit(t *testing.T) {
	c := newServer(t)
	res, body := c.do("GET", "/api/v1/audit", nil, nil)
	expect(t, res, body, http.StatusUnauthorized, "unauthenticated")

	c.setupAndLogin()
	id := c.createAndWait(validServer("Audited"))["id"].(string)

	events := func(query string) ([]map[string]any, any) {
		t.Helper()
		res, body := c.do("GET", "/api/v1/audit"+query, nil, nil)
		expect(t, res, body, http.StatusOK, "")
		var out []map[string]any
		for _, e := range body["events"].([]any) {
			out = append(out, e.(map[string]any))
		}
		return out, body["next_before"]
	}

	all, next := events("")
	if len(all) < 3 || next != nil {
		t.Fatalf("all = %v, next = %v", all, next)
	}
	first := all[0]
	for _, key := range []string{"id", "created_at", "action", "target", "ip", "success", "user", "server_id", "server_name"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("event without %q: %v", key, first)
		}
	}

	page, next := events("?limit=1")
	if len(page) != 1 || next != page[0]["id"] {
		t.Fatalf("page = %v, next = %v", page, next)
	}
	older, _ := events("?limit=1&before=" + jsonNumber(next))
	if len(older) != 1 || older[0]["id"] == page[0]["id"] {
		t.Fatalf("older page = %v", older)
	}

	created, _ := events("?server_id=" + id + "&action=server.create")
	if len(created) != 1 || created[0]["server_id"] != id || created[0]["server_name"] != "Audited" ||
		created[0]["user"] != "admin" || created[0]["success"] != true {
		t.Fatalf("server.create = %v", created)
	}
	logins, _ := events("?action=auth.login")
	if len(logins) != 1 || logins[0]["server_id"] != nil || logins[0]["server_name"] != nil {
		t.Fatalf("logins = %v", logins)
	}
	if failed, _ := events("?failed=true"); len(failed) != 0 {
		t.Fatalf("failed = %v", failed)
	}

	for query, field := range map[string]string{
		"?limit=0":        "limit",
		"?limit=201":      "limit",
		"?limit=abc":      "limit",
		"?before=-3":      "before",
		"?action=nope":    "action",
		"?server_id=../x": "server_id",
		"?failed=yes":     "failed",
	} {
		res, body := c.do("GET", "/api/v1/audit"+query, nil, nil)
		expect(t, res, body, http.StatusUnprocessableEntity, "validation_failed")
		if got := body["error"].(map[string]any)["field"]; got != field {
			t.Errorf("%s: field = %v, want %s", query, got, field)
		}
	}
}

func jsonNumber(v any) string {
	return strconv.FormatInt(int64(v.(float64)), 10)
}

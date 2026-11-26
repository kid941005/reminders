package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"smallgo/server/reminder"
)

// TestAPIRouteSnapshot locks the HTTP surface so a rename or accidental route
// drop cannot slip through code review. Regenerate with:
//
//	UPDATE_API_CONTRACT=1 go test ./server -run TestAPIRouteSnapshot
func TestAPIRouteSnapshot(t *testing.T) {
	r, _, _ := setupTestRouter(t)
	lines := make([]string, 0, len(r.Routes()))
	for _, route := range r.Routes() {
		lines = append(lines, route.Method+" "+route.Path)
	}
	sort.Strings(lines)
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("..", "..", "docs", "api-routes.txt")
	if os.Getenv("UPDATE_API_CONTRACT") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read api route snapshot: %v (run UPDATE_API_CONTRACT=1 go test ./server -run TestAPIRouteSnapshot)", err)
	}
	if string(want) != got {
		t.Fatalf("API routes changed; update docs/api-routes.txt intentionally.\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

var tsFieldRE = regexp.MustCompile(`(?m)^\s*([a-z_][a-z0-9_]*)\??:\s`)

func tsInterfaceFields(t *testing.T, name string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "api", "reminder.ts"))
	if err != nil {
		t.Fatalf("read reminder.ts: %v", err)
	}
	text := string(raw)
	start := strings.Index(text, "export interface "+name+" {")
	if start < 0 {
		t.Fatalf("interface %s not found in reminder.ts", name)
	}
	body := text[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	out := map[string]bool{}
	for _, m := range tsFieldRE.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

func goJSONFields(t *testing.T, value interface{}) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(value)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	out := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(tp reflect.Type) {
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name != "" {
				out[name] = true
			}
		}
	}
	walk(typ)
	return out
}

// TestReminderDTOContractMatchesFrontend catches the exact class of bug the
// architecture review flagged: a Go JSON tag renamed without touching the
// hand-written TypeScript interface (or vice versa).
func TestReminderDTOContractMatchesFrontend(t *testing.T) {
	cases := []struct {
		iface string
		value interface{}
	}{
		{"ReminderItem", reminder.ReminderDTO{}},
		{"SaveReminderInput", reminder.SaveReminderInput{}},
	}
	for _, tc := range cases {
		wantTS := tsInterfaceFields(t, tc.iface)
		gotGo := goJSONFields(t, tc.value)
		for field := range wantTS {
			if !gotGo[field] {
				t.Errorf("%s.%s exists in TypeScript but not in the Go JSON contract", tc.iface, field)
			}
		}
	}
}

func TestRuntimeConfigEndpointShape(t *testing.T) {
	// Guard the B7 report shape: the endpoint must stay read-only metadata and
	// must not start returning secrets such as jwt_secret.
	r, _, _ := setupTestRouter(t)
	if rec := doJSON(r, "GET", "/api/runtime-config", "", nil); rec.Code == 200 {
		t.Fatal("runtime-config should require admin, got 200 anonymously")
	}
	token := registerUser(t, r, "admin", "secret123")
	rec := doJSON(r, "GET", "/api/runtime-config", token, nil)
	if rec.Code != 200 {
		t.Fatalf("runtime-config status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "jwt_secret") {
		t.Fatalf("runtime-config leaked jwt_secret: %s", rec.Body.String())
	}
	var payload struct {
		Data struct {
			EnvOnly map[string]interface{} `json:"env_only"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode runtime-config: %v", err)
	}
	for _, key := range []string{"port", "data_dir", "db_path", "rate_limit", "cors_origin"} {
		if _, ok := payload.Data.EnvOnly[key]; !ok {
			t.Errorf("runtime-config missing env_only.%s", key)
		}
	}
}

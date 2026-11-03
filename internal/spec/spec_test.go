package spec

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func load(t *testing.T) *Spec {
	t.Helper()
	data, err := os.ReadFile("../../openapi/api.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	s, err := Load(data)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return s
}

func find(t *testing.T, s *Spec, method, path string) *Operation {
	t.Helper()
	for i := range s.Operations {
		if s.Operations[i].Method == method && s.Operations[i].Path == path {
			return &s.Operations[i]
		}
	}
	t.Fatalf("operation %s %s not found", method, path)
	return nil
}

func TestDefaultHost(t *testing.T) {
	if got := load(t).DefaultHost; got != "https://api.superdocu.com" {
		t.Errorf("DefaultHost = %q", got)
	}
}

func TestNoNameCollisionsWithinGroup(t *testing.T) {
	s := load(t)
	seen := map[string]string{}
	for _, o := range s.Operations {
		key := o.Group + "/" + o.Name
		if prev, ok := seen[key]; ok {
			t.Errorf("collision %s: %s and %s %s", key, prev, o.Method, o.Path)
		}
		seen[key] = o.Method + " " + o.Path
	}
}

func TestCommandNames(t *testing.T) {
	s := load(t)
	cases := []struct {
		method, path, group, name string
	}{
		{"get", "/api/v2/contacts", "contacts", "list"},
		{"get", "/api/v2/contacts/trash", "contacts", "trash"},
		{"post", "/api/v2/contacts", "contacts", "create"},
		{"get", "/api/v2/contacts/{id}", "contacts", "get"},
		{"post", "/api/v2/contacts/{id}/invite", "contacts", "invite"},
		{"post", "/api/v2/contacts/tags", "contact-tags", "create"},
		{"get", "/api/v2/documents/{id}/file", "files", "documents-file"},
		{"get", "/api/v2/contacts/{id}/timeline", "timeline", "contacts-timeline-by-id"},
		{"get", "/api/v2/contacts/timeline", "timeline", "contacts-timeline"},
		{"post", "/api/v2/documents_groups/{id}/not_applicable/approve", "documents-groups", "not-applicable-approve"},
		{"patch", "/api/v2/documents_groups/{id}/expiration_date", "documents-groups", "expiration-date"},
		{"post", "/api/v2/contacts/{id}/workflows/{workflow_id}/states/{state_id}/custom_documents_groups", "custom-documents-groups", "create"},
	}
	for _, c := range cases {
		op := find(t, s, c.method, c.path)
		if op.Group != c.group || op.Name != c.name {
			t.Errorf("%s %s => %s/%s, want %s/%s", c.method, c.path, op.Group, op.Name, c.group, c.name)
		}
	}
}

func collectionGet(tag string) map[string]any {
	return map[string]any{"get": map[string]any{
		"tags": []any{tag},
		"responses": map[string]any{"200": map[string]any{"content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/ItemCollection"}},
		}}},
	}}
}

func loadPaths(t *testing.T, paths map[string]any) *Spec {
	t.Helper()
	data, err := yaml.Marshal(map[string]any{"paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilteredCollectionKeepsTheListName(t *testing.T) {
	cases := []struct {
		scenario string
		paths    map[string]any
		want     map[string]string
	}{
		{
			"a filtered view of a list is named after its segment",
			map[string]any{
				"/api/v2/contacts":       collectionGet("[v2] Contacts"),
				"/api/v2/contacts/trash": collectionGet("[v2] Contacts"),
				"/api/v2/contacts/tags":  collectionGet("[v2] Contact tags"),
			},
			map[string]string{
				"/api/v2/contacts":       "contacts list",
				"/api/v2/contacts/trash": "contacts trash",
				"/api/v2/contacts/tags":  "contact-tags list",
			},
		},
		{
			"a filtered view of a nested list keeps the nested list name",
			map[string]any{
				"/api/v2/contacts/{id}/workflows":        collectionGet("[v2] Contact workflows"),
				"/api/v2/contacts/{id}/workflows/active": collectionGet("[v2] Contact workflows"),
			},
			map[string]string{
				"/api/v2/contacts/{id}/workflows":        "contact-workflows list",
				"/api/v2/contacts/{id}/workflows/active": "contact-workflows active",
			},
		},
		{
			"a lone list with a trailing slash stays list",
			map[string]any{"/api/v2/contacts/tags/": collectionGet("[v2] Contacts")},
			map[string]string{"/api/v2/contacts/tags/": "contacts list"},
		},
		{
			"a lone list whose tag has no name stays list",
			map[string]any{"/api/v2/contacts/tags": collectionGet("[v2]")},
			map[string]string{"/api/v2/contacts/tags": " list"},
		},
	}
	for _, c := range cases {
		s := loadPaths(t, c.paths)
		for path, want := range c.want {
			op := find(t, s, "get", path)
			if got := op.Group + " " + op.Name; got != want {
				t.Errorf("%s: get %s => %q, want %q", c.scenario, path, got, want)
			}
		}
	}
}

func TestPathParamOrder(t *testing.T) {
	op := find(t, load(t), "get", "/api/v2/contacts/{id}/workflows/{wid}")
	if len(op.PathParams) != 2 || op.PathParams[0].Name != "id" || op.PathParams[1].Name != "wid" {
		t.Errorf("path params = %+v", op.PathParams)
	}
}

func TestJSONBody(t *testing.T) {
	op := find(t, load(t), "post", "/api/v2/contacts")
	if op.Body == nil {
		t.Fatal("expected JSON body")
	}
	if op.Body.DataType != "contact" {
		t.Errorf("DataType = %q", op.Body.DataType)
	}
	if !hasAttr(op.Body.Attrs, "email") {
		t.Errorf("missing email attr: %+v", op.Body.Attrs)
	}
}

func TestMultipartBodies(t *testing.T) {
	s := load(t)

	if mp := find(t, s, "post", "/api/v2/contacts/import").Multipart; mp == nil || !mp.BareBinary {
		t.Errorf("import should be bare-binary multipart: %+v", mp)
	}
	if mp := find(t, s, "post", "/api/v2/documents_groups/{id}/documents").Multipart; mp == nil || !mp.ArrayBinary {
		t.Errorf("documents upload should be array-binary multipart: %+v", mp)
	}
	shared := find(t, s, "post", "/api/v2/contacts/{id}/shared_documents").Multipart
	if shared == nil || len(shared.Files) != 1 || shared.Files[0].Name != "file" {
		t.Errorf("shared_documents should expose a 'file' field: %+v", shared)
	}
	if !hasAttr(shared.TextFields, "name") {
		t.Errorf("shared_documents should expose a 'name' text field: %+v", shared.TextFields)
	}
}

func hasAttr(attrs []BodyAttr, name string) bool {
	for _, a := range attrs {
		if a.Name == name {
			return true
		}
	}
	return false
}

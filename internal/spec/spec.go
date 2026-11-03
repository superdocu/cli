// Package spec turns the embedded OpenAPI document into a flat list of
// operations the CLI can mount as Cobra commands. Everything the command tree
// knows about the API is derived here from the spec, so regenerating the spec
// (make gen) keeps the CLI in sync with the API surface automatically.
package spec

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Param is a path, query or header parameter.
type Param struct {
	Name        string
	In          string
	Required    bool
	Description string
	Type        string
	Enum        []string
}

// BodyAttr is one attribute under data.attributes of a JSON:API request body
// (or a text field of a multipart body).
type BodyAttr struct {
	Name        string
	Type        string
	Required    bool
	Description string
	Enum        []string
}

// Body describes a JSON:API request body.
type Body struct {
	DataType string // value for data.type, when the schema pins a single enum
	Attrs    []BodyAttr
}

// FileField is a named binary property of a multipart body.
type FileField struct {
	Name string
}

// Multipart describes a multipart/form-data request body.
type Multipart struct {
	TextFields  []BodyAttr
	Files       []FileField
	BareBinary  bool // schema is a single bare binary (e.g. CSV import)
	ArrayBinary bool // schema is an array of binaries (multi-file upload)
}

// Operation is a single API endpoint+method, ready to mount as a command.
type Operation struct {
	Method       string
	Path         string
	Summary      string
	Description  string
	Group        string
	Name         string
	PathParams   []Param
	QueryParams  []Param
	IsCollection bool
	Body         *Body
	Multipart    *Multipart
}

// Spec is the parsed, command-ready view of the OpenAPI document.
type Spec struct {
	Title       string
	Version     string
	DefaultHost string
	Operations  []Operation
	Groups      []string
}

var httpMethods = []string{"get", "post", "patch", "put", "delete"}

// Load parses the embedded OpenAPI YAML into a Spec.
func Load(data []byte) (*Spec, error) {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	s := &Spec{}
	if info := asMap(root["info"]); info != nil {
		s.Title = asStr(info["title"])
		s.Version = asStr(info["version"])
	}
	s.DefaultHost = parseDefaultHost(root)

	for path, pv := range asMap(root["paths"]) {
		item := asMap(pv)
		shared := parseParams(asSlice(item["parameters"]))
		for _, m := range httpMethods {
			ov := item[m]
			if ov == nil {
				continue
			}
			op := asMap(ov)
			o := Operation{
				Method:      m,
				Path:        path,
				Summary:     asStr(op["summary"]),
				Description: asStr(op["description"]),
				Group:       Kebab(strings.TrimSpace(strings.TrimPrefix(firstTag(op), "[v2]"))),
			}
			all := append(append([]Param{}, shared...), parseParams(asSlice(op["parameters"]))...)
			for _, p := range all {
				switch p.In {
				case "path":
					o.PathParams = append(o.PathParams, p)
				case "query":
					o.QueryParams = append(o.QueryParams, p)
				}
			}
			sortPathParams(&o)
			o.IsCollection = returnsCollection(op)
			parseBody(op, &o)
			s.Operations = append(s.Operations, o)
		}
	}

	s.assignNames()
	s.collectGroups()
	return s, nil
}

func parseDefaultHost(root map[string]any) string {
	for _, sv := range asSlice(root["servers"]) {
		m := asMap(sv)
		url := asStr(m["url"])
		if vars := asMap(m["variables"]); vars != nil {
			if h := asMap(vars["host"]); h != nil {
				url = strings.ReplaceAll(url, "{host}", asStr(h["default"]))
			}
		}
		if url != "" {
			return strings.TrimRight(url, "/")
		}
	}
	return ""
}

func parseParams(list []any) []Param {
	var out []Param
	for _, pv := range list {
		p := asMap(pv)
		sc := asMap(p["schema"])
		out = append(out, Param{
			Name:        asStr(p["name"]),
			In:          asStr(p["in"]),
			Required:    asBool(p["required"]),
			Description: asStr(p["description"]),
			Type:        asStr(sc["type"]),
			Enum:        strSlice(sc["enum"]),
		})
	}
	return out
}

func parseBody(op map[string]any, o *Operation) {
	content := asMap(asMap(op["requestBody"])["content"])
	if content == nil {
		return
	}
	if j := asMap(content["application/json"]); j != nil {
		o.Body = parseJSONBody(asMap(j["schema"]))
		return
	}
	if mp := asMap(content["multipart/form-data"]); mp != nil {
		o.Multipart = parseMultipart(asMap(mp["schema"]))
	}
}

func parseJSONBody(schema map[string]any) *Body {
	data := asMap(asMap(schema["properties"])["data"])
	dprops := asMap(data["properties"])
	b := &Body{}
	if en := strSlice(asMap(dprops["type"])["enum"]); len(en) == 1 {
		b.DataType = en[0]
	}
	attrs := asMap(dprops["attributes"])
	required := map[string]bool{}
	for _, r := range strSlice(attrs["required"]) {
		required[r] = true
	}
	for name, pv := range asMap(attrs["properties"]) {
		ps := asMap(pv)
		b.Attrs = append(b.Attrs, BodyAttr{
			Name:        name,
			Type:        asStr(ps["type"]),
			Required:    required[name],
			Description: asStr(ps["description"]),
			Enum:        strSlice(ps["enum"]),
		})
	}
	sort.Slice(b.Attrs, func(i, j int) bool { return b.Attrs[i].Name < b.Attrs[j].Name })
	return b
}

func parseMultipart(schema map[string]any) *Multipart {
	mp := &Multipart{}
	switch asStr(schema["type"]) {
	case "string":
		if asStr(schema["format"]) == "binary" {
			mp.BareBinary = true
			return mp
		}
	case "array":
		if asStr(asMap(schema["items"])["format"]) == "binary" {
			mp.ArrayBinary = true
			return mp
		}
	}
	required := map[string]bool{}
	for _, r := range strSlice(schema["required"]) {
		required[r] = true
	}
	for name, pv := range asMap(schema["properties"]) {
		ps := asMap(pv)
		if asStr(ps["format"]) == "binary" {
			mp.Files = append(mp.Files, FileField{Name: name})
			continue
		}
		mp.TextFields = append(mp.TextFields, BodyAttr{
			Name:        name,
			Type:        asStr(ps["type"]),
			Required:    required[name],
			Description: asStr(ps["description"]),
		})
	}
	return mp
}

func returnsCollection(op map[string]any) bool {
	for code, rv := range asMap(op["responses"]) {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		schema := asMap(asMap(asMap(asMap(rv)["content"])["application/json"])["schema"])
		if strings.HasSuffix(asStr(schema["$ref"]), "Collection") {
			return true
		}
	}
	return false
}

func firstTag(op map[string]any) string {
	if t := strSlice(op["tags"]); len(t) > 0 {
		return t[0]
	}
	return "general"
}

// assignNames derives a stable, collision-free command name for every operation.
func (s *Spec) assignNames() {
	collectionGet := map[string]string{}
	for _, o := range s.Operations {
		if o.Method == "get" && o.IsCollection {
			collectionGet[strings.Trim(o.Path, "/")] = o.Group
		}
	}
	byGroup := map[string][]*Operation{}
	for i := range s.Operations {
		o := &s.Operations[i]
		o.Name = computeName(o, collectionGet)
		byGroup[o.Group] = append(byGroup[o.Group], o)
	}
	for _, ops := range byGroup {
		resolveCollisions(ops)
	}
}

func computeName(o *Operation, collectionGet map[string]string) string {
	segs := splitPath(o.Path)
	lastParam := -1
	for i, sg := range segs {
		if isParam(sg) {
			lastParam = i
		}
	}
	var tail []string
	switch {
	case lastParam >= 0:
		tail = segs[lastParam+1:]
	case len(segs) > 1:
		tail = segs[1:]
	}

	verb := func() string {
		switch o.Method {
		case "get":
			if o.IsCollection {
				return "list"
			}
			return "get"
		case "post":
			return "create"
		case "patch", "put":
			return "update"
		default:
			return "delete"
		}
	}

	if len(tail) == 0 {
		return verb()
	}
	if o.Method == "get" && o.IsCollection {
		fullSegs := strings.Split(strings.Trim(o.Path, "/"), "/")
		for depth := 1; depth <= len(tail); depth++ {
			parent := strings.Join(fullSegs[:len(fullSegs)-depth], "/")
			if group, ok := collectionGet[parent]; ok && group == o.Group {
				return Kebab(strings.Join(tail[len(tail)-depth:], "-"))
			}
		}
		return "list"
	}
	if _, ok := collectionGet[strings.Trim(o.Path, "/")]; ok && o.Method == "post" {
		return "create"
	}
	name := Kebab(strings.Join(tail, "-"))
	if name == o.Group {
		return verb()
	}
	return name
}

func resolveCollisions(ops []*Operation) {
	counts := func() map[string]int {
		m := map[string]int{}
		for _, o := range ops {
			m[o.Name]++
		}
		return m
	}

	c := counts()
	for _, o := range ops {
		if c[o.Name] > 1 {
			o.Name = Kebab(strings.Join(literalSegs(o.Path), "-"))
		}
	}
	c = counts()
	for _, o := range ops {
		if c[o.Name] > 1 {
			if lp := lastParamName(o.Path); lp != "" {
				o.Name = o.Name + "-by-" + Kebab(lp)
			}
		}
	}
	seen := map[string]int{}
	for _, o := range ops {
		seen[o.Name]++
		if seen[o.Name] > 1 {
			o.Name = fmt.Sprintf("%s-%d", o.Name, seen[o.Name])
		}
	}
}

func (s *Spec) collectGroups() {
	set := map[string]bool{}
	for _, o := range s.Operations {
		set[o.Group] = true
	}
	for g := range set {
		s.Groups = append(s.Groups, g)
	}
	sort.Strings(s.Groups)
}

func sortPathParams(o *Operation) {
	sort.SliceStable(o.PathParams, func(i, j int) bool {
		return strings.Index(o.Path, "{"+o.PathParams[i].Name+"}") <
			strings.Index(o.Path, "{"+o.PathParams[j].Name+"}")
	})
}

// path helpers

func splitPath(p string) []string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for len(parts) > 0 && (parts[0] == "api" || parts[0] == "v2") {
		parts = parts[1:]
	}
	return parts
}

func isParam(s string) bool { return strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") }

func literalSegs(p string) []string {
	var out []string
	for _, s := range splitPath(p) {
		if !isParam(s) {
			out = append(out, s)
		}
	}
	return out
}

func lastParamName(p string) string {
	name := ""
	for _, s := range splitPath(p) {
		if isParam(s) {
			name = strings.Trim(s, "{}")
		}
	}
	return name
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Kebab lowercases and hyphenates, used for group, command and flag names.
func Kebab(s string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// generic YAML navigation helpers

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asSlice(v any) []any        { s, _ := v.([]any); return s }
func asStr(v any) string         { s, _ := v.(string); return s }
func asBool(v any) bool          { b, _ := v.(bool); return b }

func strSlice(v any) []string {
	s, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(s))
	for _, e := range s {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

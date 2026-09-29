package ide

import (
	"encoding/json"
	"strings"
)

// Schema is the part of an OpenAPI v3 schema that the checks and the field
// help use.
type Schema struct {
	Type            string             `json:"type"`
	Format          string             `json:"format"`
	Description     string             `json:"description"`
	Properties      map[string]*Schema `json:"properties"`
	Items           *Schema            `json:"items"`
	Additional      *Additional        `json:"additionalProperties"`
	Ref             string             `json:"$ref"`
	AllOf           []*Schema          `json:"allOf"`
	OneOf           []*Schema          `json:"oneOf"`
	AnyOf           []*Schema          `json:"anyOf"`
	Required        []string           `json:"required"`
	Enum            []any              `json:"enum"`
	PreserveUnknown bool               `json:"x-kubernetes-preserve-unknown-fields"`
	IntOrString     bool               `json:"x-kubernetes-int-or-string"`
	GVK             []GVK              `json:"x-kubernetes-group-version-kind"`
}

// GVK names a group, version and kind.
type GVK struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// Additional is the additionalProperties value: a boolean or a schema.
type Additional struct {
	Allowed bool
	Schema  *Schema
}

// UnmarshalJSON reads a boolean or a schema.
func (a *Additional) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		a.Allowed = v
		return nil
	}
	a.Allowed = true
	a.Schema = &Schema{}
	return json.Unmarshal(b, a.Schema)
}

// SchemaDoc is one OpenAPI v3 document of a group version.
type SchemaDoc struct {
	schemas map[string]*Schema
	kinds   map[string]string // kind to schema name
}

// ParseSchemaDoc reads an OpenAPI v3 document for one group version.
func ParseSchemaDoc(b []byte, groupVersion string) (*SchemaDoc, error) {
	var raw struct {
		Components struct {
			Schemas map[string]*Schema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	group, version := splitAPIVersion(groupVersion)
	d := &SchemaDoc{schemas: raw.Components.Schemas, kinds: map[string]string{}}
	for name, s := range d.schemas {
		for _, g := range s.GVK {
			if g.Group == group && g.Version == version {
				d.kinds[g.Kind] = name
			}
		}
	}
	return d, nil
}

// Kind returns the schema of a kind, or nil.
func (d *SchemaDoc) Kind(kind string) *Schema {
	if d == nil {
		return nil
	}
	return d.schemas[d.kinds[kind]]
}

// Resolved is a schema with its references followed. The description of
// the field wins over the description of the referenced type.
type Resolved struct {
	*Schema
	Desc      string
	FieldDesc bool   // Desc describes the field, not only its type
	Ref       string // the short name of the referenced type, such as Container
}

// Resolve follows $ref and single allOf references.
func (d *SchemaDoc) Resolve(s *Schema) Resolved {
	r := Resolved{Schema: s}
	if s == nil {
		return r
	}
	r.Desc = s.Description
	r.FieldDesc = r.Desc != ""
	for i := 0; i < 8 && r.Schema != nil; i++ {
		ref := r.Schema.Ref
		if ref == "" && len(r.Schema.AllOf) == 1 && r.Schema.Type == "" && len(r.Schema.Properties) == 0 {
			ref = r.Schema.AllOf[0].Ref
		}
		if ref == "" {
			break
		}
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		r.Ref = name[strings.LastIndex(name, ".")+1:]
		r.Schema = d.schemas[name]
		if r.Desc == "" && r.Schema != nil {
			r.Desc = r.Schema.Description
		}
	}
	return r
}

// Child returns the schema of a key or a list item below a schema.
func (d *SchemaDoc) Child(s *Schema, seg string) *Schema {
	r := d.Resolve(s)
	if r.Schema == nil {
		return nil
	}
	if strings.HasPrefix(seg, "[") {
		return r.Items
	}
	if p := r.Properties[seg]; p != nil {
		return p
	}
	if r.Additional != nil {
		return r.Additional.Schema
	}
	return nil
}

// At returns the schema at a path below a kind schema.
func (d *SchemaDoc) At(root *Schema, path []string) *Schema {
	s := root
	for _, seg := range path {
		s = d.Child(s, seg)
		if s == nil {
			return nil
		}
	}
	return s
}

// TypeName describes a schema the way kubectl explain does, for example
// integer, []Container or map[string]string.
func (d *SchemaDoc) TypeName(s *Schema) string {
	r := d.Resolve(s)
	if r.Schema == nil {
		return ""
	}
	switch {
	case r.Ref == "Quantity":
		return "Quantity"
	case r.IntOrString || r.Ref == "IntOrString":
		return "int | string"
	case r.Ref != "" && r.Type == "object":
		return r.Ref
	case r.Type == "array":
		if n := d.TypeName(r.Items); n != "" {
			return "[]" + n
		}
		return "array"
	case r.Additional != nil && r.Additional.Schema != nil && len(r.Properties) == 0:
		return "map[string]" + d.TypeName(r.Additional.Schema)
	case r.Ref != "" && r.Type == "":
		return r.Ref
	case r.Type == "":
		return "object"
	}
	if r.Format != "" && r.Format != "int32" && r.Format != "int64" && r.Format != "double" {
		return r.Type + " (" + r.Format + ")"
	}
	return r.Type
}

func splitAPIVersion(apiVersion string) (group, version string) {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i], apiVersion[i+1:]
	}
	return "", apiVersion
}

// firstSentences shortens a description for the inspector.
func firstSentences(s string, max int) string {
	for _, cut := range []string{"```", "More info:"} {
		if i := strings.Index(s, cut); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max], ". ")
	if cut < max/3 {
		cut = strings.LastIndex(s[:max], " ")
		return s[:cut] + " …"
	}
	return s[:cut+1]
}

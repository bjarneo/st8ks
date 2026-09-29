package ide

import (
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Doc is one YAML document of a file. All lines and columns are 0-based and
// count from the start of the file.
type Doc struct {
	Start, End int        // the first line and the line after the last one
	Root       *yaml.Node // the top node, or nil for a broken document
	Err        string
	ErrLine    int

	APIVersion string
	Kind       string
	Name       string
	Namespace  string

	ends   map[*yaml.Node]int // the last line of each value node
	fields []*Field           // the deepest field that starts on each line
}

// Field is a key or a list item of a document, with its path.
type Field struct {
	Line int
	Path []string // for example spec, containers, [0], image
	Key  *yaml.Node
	Val  *yaml.Node
}

// PathString joins a path the way kubectl explain prints it.
func PathString(p []string) string {
	var b strings.Builder
	for i, s := range p {
		if i > 0 && !strings.HasPrefix(s, "[") {
			b.WriteByte('.')
		}
		b.WriteString(s)
	}
	return b.String()
}

// IsObject reports if the document looks like a Kubernetes object.
func (d *Doc) IsObject() bool {
	return d.Root != nil && d.Root.Kind == yaml.MappingNode && d.APIVersion != "" && d.Kind != ""
}

// FieldAt returns the field that starts on a line, or nil.
func (d *Doc) FieldAt(line int) *Field {
	i := line - d.Start
	if i < 0 || i >= len(d.fields) {
		return nil
	}
	return d.fields[i]
}

// EndOf returns the last line of a value node.
func (d *Doc) EndOf(n *yaml.Node) int {
	if e, ok := d.ends[n]; ok {
		return e
	}
	return n.Line
}

var (
	docSep  = regexp.MustCompile(`^---(\s|$)`)
	errLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)
)

// ParseDocs splits a file into documents and parses each one alone, so an
// error in one document does not hide the others.
func ParseDocs(text string) []*Doc {
	lines := strings.Split(text, "\n")
	var docs []*Doc
	start := 0
	flush := func(end int) {
		if end <= start || !hasContent(lines[start:end]) {
			return
		}
		docs = append(docs, parseDoc(lines, start, end))
	}
	for i, l := range lines {
		if docSep.MatchString(l) || strings.TrimRight(l, " \t\r") == "..." {
			flush(i)
			start = i + 1
		}
	}
	flush(len(lines))
	return docs
}

func hasContent(lines []string) bool {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" && !strings.HasPrefix(t, "#") {
			return true
		}
	}
	return false
}

func parseDoc(lines []string, start, end int) *Doc {
	d := &Doc{Start: start, End: end, ends: map[*yaml.Node]int{}}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[start:end], "\n")), &n); err != nil {
		d.Err, d.ErrLine = yamlError(err.Error(), start)
		return d
	}
	if n.Kind != yaml.DocumentNode || len(n.Content) == 0 {
		return d
	}
	shift(&n, start)
	d.Root = n.Content[0]
	last := trimEnd(lines, end-1, d.Root.Line)
	d.setEnds(lines, d.Root, last)
	d.fields = make([]*Field, end-start)
	d.index(nil, d.Root)
	if d.Root.Kind == yaml.MappingNode {
		d.APIVersion = scalar(d.Root, "apiVersion")
		d.Kind = scalar(d.Root, "kind")
		if md := get(d.Root, "metadata"); md != nil {
			d.Name = scalar(md, "name")
			d.Namespace = scalar(md, "namespace")
		}
	}
	return d
}

// yamlError turns a parser message into a sentence and a line.
func yamlError(msg string, start int) (string, int) {
	line := start
	if m := errLine.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			line = start + n - 1
		}
		msg = m[2]
	} else {
		msg = strings.TrimPrefix(msg, "yaml: ")
	}
	if msg != "" {
		msg = strings.ToUpper(msg[:1]) + msg[1:]
	}
	return "YAML syntax error: " + msg, line
}

// shift makes the lines and columns of every node 0-based and relative to
// the file.
func shift(n *yaml.Node, start int) {
	n.Line += start - 1
	n.Column--
	for _, c := range n.Content {
		shift(c, start)
	}
}

// trimEnd moves the end of a range up over blank and comment lines, but not
// above min.
func trimEnd(lines []string, end, min int) int {
	for end > min {
		t := strings.TrimSpace(lines[end])
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		end--
	}
	return end
}

// setEnds records the last line of every node. A value ends before the next
// key or list item of its parent.
func (d *Doc) setEnds(lines []string, n *yaml.Node, end int) {
	if end < n.Line {
		end = n.Line
	}
	d.ends[n] = end
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			next := end + 1
			if i+2 < len(n.Content) {
				next = n.Content[i+2].Line
			}
			d.setEnds(lines, n.Content[i+1], trimEnd(lines, next-1, n.Content[i+1].Line))
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			next := end + 1
			if i+1 < len(n.Content) {
				next = n.Content[i+1].Line
			}
			d.setEnds(lines, c, trimEnd(lines, next-1, c.Line))
		}
	}
}

// index records the deepest field that starts on each line.
func (d *Doc) index(path []string, n *yaml.Node) {
	set := func(f *Field) {
		i := f.Line - d.Start
		if i >= 0 && i < len(d.fields) {
			d.fields[i] = f
		}
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			p := append(append([]string{}, path...), k.Value)
			set(&Field{Line: k.Line, Path: p, Key: k, Val: v})
			d.index(p, v)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			p := append(append([]string{}, path...), "["+strconv.Itoa(i)+"]")
			if c.Kind == yaml.ScalarNode || c.Kind == yaml.AliasNode {
				set(&Field{Line: c.Line, Path: p, Val: c})
			}
			d.index(p, c)
		}
	}
}

// get returns the value of a key in a mapping node.
func get(n *yaml.Node, key string) *yaml.Node {
	_, v := pair(n, key)
	return v
}

// pair returns the key and the value node of a key in a mapping node.
func pair(n *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i], n.Content[i+1]
		}
	}
	return nil, nil
}

// scalar returns the string value of a key, or "".
func scalar(n *yaml.Node, key string) string {
	v := get(n, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

// dig follows a path of keys through mapping nodes.
func dig(n *yaml.Node, keys ...string) *yaml.Node {
	for _, k := range keys {
		n = get(n, k)
		if n == nil {
			return nil
		}
	}
	return n
}

// items returns the items of a sequence node.
func items(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// stringItems returns the scalar items of a sequence node.
func stringItems(n *yaml.Node) []*yaml.Node {
	var out []*yaml.Node
	for _, c := range items(n) {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c)
		}
	}
	return out
}

// spanEnd returns the column after the value that starts at col on a line,
// without a trailing comment.
func spanEnd(line string, col int) int {
	if col >= len(line) {
		return len(line)
	}
	q := byte(0)
	for i := col; i < len(line); i++ {
		c := line[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			if i == col {
				q = c
			}
		case c == '#' && i > col && (line[i-1] == ' ' || line[i-1] == '\t'):
			return len(strings.TrimRight(line[:i], " \t"))
		}
	}
	return len(strings.TrimRight(line, " \t\r"))
}

// indentOf returns the leading whitespace of a line.
func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

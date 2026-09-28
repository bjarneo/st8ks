package kube

import (
	"sync"
)

// table holds the rows of one kind. Rows are immutable after insert, so
// readers can share the pointers without a copy.
type table struct {
	name string

	mu      sync.Mutex
	kind    *Kind
	watched bool
	rows    map[string]*Row
	dirty   map[string]struct{}
	counts  map[string]*[2]int
	cDirty  bool
	sDirty  bool
	version uint64
	synced  bool
	err     string
}

func newTable(k *Kind) *table {
	return &table{name: k.Name, kind: k, rows: map[string]*Row{}, dirty: map[string]struct{}{}, counts: map[string]*[2]int{}}
}

func (t *table) count(r *Row, d int) {
	c := t.counts[r.N]
	if c == nil {
		c = &[2]int{}
		t.counts[r.N] = c
	}
	c[0] += d
	if r.B {
		c[1] += d
	}
	if c[0] == 0 {
		delete(t.counts, r.N)
	}
	t.cDirty = true
}

func (t *table) upsert(r *Row) {
	if r == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	old := t.rows[r.U]
	if old != nil {
		if old.equal(r) {
			return
		}
		t.count(old, -1)
	}
	t.rows[r.U] = r
	t.count(r, 1)
	t.dirty[r.U] = struct{}{}
	t.version++
}

func (t *table) remove(uid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	old := t.rows[uid]
	if old == nil {
		return
	}
	t.count(old, -1)
	delete(t.rows, uid)
	t.dirty[uid] = struct{}{}
	t.version++
}

// retain removes every row whose UID is not in keep.
func (t *table) retain(keep map[string]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for uid, r := range t.rows {
		if _, ok := keep[uid]; !ok {
			t.count(r, -1)
			delete(t.rows, uid)
			t.dirty[uid] = struct{}{}
			t.version++
		}
	}
}

func (t *table) setSynced(v bool) {
	t.mu.Lock()
	if t.synced != v {
		t.synced = v
		t.sDirty = true
		t.version++
	}
	t.mu.Unlock()
}

func (t *table) setErr(e string) {
	t.mu.Lock()
	if t.err != e {
		t.err = e
		t.sDirty = true
		t.version++
	}
	t.mu.Unlock()
}

func (t *table) get(uid string) *Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rows[uid]
}

func (t *table) snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	rows := make([]*Row, 0, len(t.rows))
	for _, r := range t.rows {
		rows = append(rows, r)
	}
	return Snapshot{Kind: t.name, V: t.version, Rows: rows, Synced: t.synced, Err: t.err, Available: true}
}

// setWatched marks whether the frontend receives this table's changes.
// The flag lives under the table lock, so a change can never fall between
// the watch and the next flush.
func (t *table) setWatched(w bool) {
	t.mu.Lock()
	t.watched = w
	t.mu.Unlock()
}

// replaceKind empties the table for a kind whose printer changed. The
// version keeps growing, so the frontend receives the deletions.
func (t *table) replaceKind(k *Kind) {
	t.mu.Lock()
	t.kind = k
	for uid := range t.rows {
		t.dirty[uid] = struct{}{}
	}
	t.rows = map[string]*Row{}
	t.counts = map[string]*[2]int{}
	t.cDirty = true
	t.synced = false
	t.sDirty = true
	t.version++
	t.mu.Unlock()
}

// takeDelta collects the changed rows and clears the change set. It returns
// nil when nothing changed or when nobody watches the table.
func (t *table) takeDelta() *Delta {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.dirty) == 0 && !t.sDirty {
		return nil
	}
	var d *Delta
	if t.watched {
		d = &Delta{Kind: t.name, V: t.version, Synced: t.synced, Err: t.err}
		for uid := range t.dirty {
			if r := t.rows[uid]; r != nil {
				d.Up = append(d.Up, r)
			} else {
				d.Del = append(d.Del, uid)
			}
		}
	}
	clear(t.dirty)
	t.sDirty = false
	return d
}

// takeCounts returns a copy of the counts if they changed.
func (t *table) takeCounts(force bool) (map[string][2]int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.cDirty && !force {
		return nil, false
	}
	t.cDirty = false
	out := make(map[string][2]int, len(t.counts))
	for ns, c := range t.counts {
		out[ns] = *c
	}
	return out, true
}

func (t *table) ver() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

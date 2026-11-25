package motionref

// dblist.go: the lists of references, learnings and styles that can be filtered, searched, sorted and paged (spec api-listing). The stores
// of the database do it with a query; the stores of folders do the same in memory over the whole list, which is small.

import (
	"cmp"
	"slices"
	"strings"

	"gorm.io/gorm"

	"veritmo/internal/music/musicmotion"
	"veritmo/internal/platform/store"
)

// ReferencesListSpec, LearningsListSpec and PresetsListSpec say what each list can be sorted and searched by.
var (
	ReferencesListSpec = store.ListSpec{Sortable: map[string]string{"created_at": "created_at", "title": "title"}, Searchable: []string{"title", "video"}, Default: "-created_at", Tie: "id"}
	LearningsListSpec  = store.ListSpec{Sortable: map[string]string{"created_at": "created_at"}, Default: "-created_at", Tie: "id"}
	PresetsListSpec    = store.ListSpec{Sortable: map[string]string{"created_at": "created_at", "name": "name"}, Searchable: []string{"name"}, Default: "-created_at", Tie: "id"}
)

// memoryPage filters (keep), sorts and slices a whole list the way the query of the database would: the sort asked for (or the default of the
// spec) with the id as the tie, a later id first on a descending sort.
func memoryPage[T any](all []T, p store.ListParams, spec store.ListSpec, keep func(T) bool, keys map[string]func(a, b T) int, id func(T) string) ([]T, int64) {
	var items []T
	for _, it := range all {
		if keep(it) {
			items = append(items, it)
		}
	}
	name, desc := p.Sort, p.Desc
	if name == "" {
		name, desc = strings.TrimPrefix(spec.Default, "-"), strings.HasPrefix(spec.Default, "-")
	}
	if cmpKey := keys[name]; cmpKey != nil {
		slices.SortStableFunc(items, func(a, b T) int {
			c := cmpKey(a, b)
			if c == 0 {
				c = cmp.Compare(id(a), id(b))
				if desc {
					c = -c
				}
				return c
			}
			if desc {
				return -c
			}
			return c
		})
	}
	return store.Slice(items, p), int64(len(items))
}

func textContains(q string, texts ...string) bool {
	if q == "" {
		return true
	}
	for _, t := range texts {
		if strings.Contains(strings.ToLower(t), strings.ToLower(q)) {
			return true
		}
	}
	return false
}

// Page reads the references that match the state (empty: any) and the parameters, and how many match in all.
func (s *Store) Page(state string, p store.ListParams) ([]Reference, int64, error) {
	if s.db == nil {
		out, total := memoryPage(s.List(), p, ReferencesListSpec,
			func(r Reference) bool {
				return (state == "" || r.State == state) && textContains(p.Q, r.Title, r.Source)
			},
			map[string]func(a, b Reference) int{
				"created_at": func(a, b Reference) int { return a.CreatedAt.Compare(b.CreatedAt) },
				"title":      func(a, b Reference) int { return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) },
			}, func(r Reference) string { return r.ID })
		return out, total, nil
	}
	rows, total, err := pageRows[referenceRow](s.db, p, ReferencesListSpec, state, "state")
	if err != nil {
		return nil, 0, err
	}
	out := make([]Reference, 0, len(rows))
	for _, r := range rows {
		if ref, err := referenceOfRow(r); err == nil {
			out = append(out, ref)
		}
	}
	return out, total, nil
}

// Page reads the learnings of a reference (empty: any) in a state (empty: any) that match the parameters, and how many match in all.
func (s *LearningStore) Page(referenceID, state string, p store.ListParams) ([]Learning, int64, error) {
	if s.db == nil {
		out, total := memoryPage(s.List(referenceID), p, LearningsListSpec,
			func(l Learning) bool { return state == "" || l.State == state },
			map[string]func(a, b Learning) int{"created_at": func(a, b Learning) int { return a.CreatedAt.Compare(b.CreatedAt) }},
			func(l Learning) string { return l.ID })
		return out, total, nil
	}
	base := s.db.DB.Model(&learningRow{})
	if referenceID != "" {
		base = base.Where("reference_id = ?", referenceID)
	}
	if state != "" {
		base = base.Where("state = ?", state)
	}
	var rows []learningRow
	total, err := countAndFind(base, p, LearningsListSpec, &rows)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Learning, 0, len(rows))
	for _, r := range rows {
		if l, err := s.dbGet(r.ID); err == nil {
			out = append(out, l)
		}
	}
	return out, total, nil
}

// Page reads the styles that match the parameters, and how many match in all.
func (s *PresetStore) Page(p store.ListParams) ([]musicmotion.Preset, int64, error) {
	if s.db == nil {
		out, total := memoryPage(s.List(), p, PresetsListSpec, func(x musicmotion.Preset) bool { return textContains(p.Q, x.Name) },
			map[string]func(a, b musicmotion.Preset) int{
				"created_at": func(a, b musicmotion.Preset) int { return a.CreatedAt.Compare(b.CreatedAt) },
				"name": func(a, b musicmotion.Preset) int {
					return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
				},
			}, func(x musicmotion.Preset) string { return x.ID })
		return out, total, nil
	}
	rows, total, err := pageRows[presetRow](s.db, p, PresetsListSpec, "", "")
	if err != nil {
		return nil, 0, err
	}
	out := make([]musicmotion.Preset, 0, len(rows))
	for _, r := range rows {
		if x, err := presetOfRow(r); err == nil {
			out = append(out, x)
		}
	}
	return out, total, nil
}

func pageRows[T any](st *store.Store, p store.ListParams, spec store.ListSpec, filter, column string) ([]T, int64, error) {
	var zero T
	base := st.DB.Model(&zero)
	if filter != "" {
		base = base.Where(column+" = ?", filter)
	}
	var rows []T
	total, err := countAndFind(base, p, spec, &rows)
	return rows, total, err
}

func countAndFind(base *gorm.DB, p store.ListParams, spec store.ListSpec, out any) (int64, error) {
	base = store.Filter(base, p, spec)
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return 0, err
	}
	q := store.Order(base, p, spec)
	if p.Limit > 0 {
		q = q.Limit(p.Limit)
	}
	if p.Offset > 0 {
		q = q.Offset(p.Offset)
	}
	return total, q.Find(out).Error
}

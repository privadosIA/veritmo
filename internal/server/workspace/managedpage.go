package workspace

// managedpage.go: the lists of the generation API (generations, picture edits, video edits) as paged queries on the table of records (spec
// video-generation-api, "The records of the calls are kept in the database").

import (
	"net/http"
	"strings"

	"gorm.io/gorm"

	"veritmo/internal/platform/store"
	"veritmo/internal/project/config"
	"veritmo/internal/server/api"
)

// ManagedListSpec says what the lists of records can be sorted by: created_at, newest first by default as they always were.
var ManagedListSpec = store.ListSpec{Sortable: map[string]string{"created_at": "created_at"}, Default: "-created_at"}

// ManagedPageParams reads the parameters of a list of records (limit, offset, sort) and the filter `client=a,b`; it answers the 400 itself.
func ManagedPageParams(w http.ResponseWriter, r *http.Request) (store.ListParams, []string, bool) {
	params, err := store.ParseList(r, ManagedListSpec)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return params, nil, false
	}
	var clients []string
	if v := r.URL.Query().Get("client"); v != "" {
		for _, name := range strings.Split(v, ",") {
			name = strings.TrimSpace(name)
			if name != clientEmbedded && name != ClientUnknown && !config.ValidClient(name) {
				api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, "client "+name+" is not a client name: use a comma list of names ("+config.ClientPatternText+"), embedded or unknown")
				return params, nil, false
			}
			clients = append(clients, name)
		}
	}
	return params, clients, true
}

// ManagedPage lists the records whose id starts with prefix (one kind), newest first unless a sort says otherwise, for the clients named (none: all)
// and what the content mode shows, one page of them, and how many there are in all.
func (h *Handler) ManagedPage(prefix string, clients []string, params store.ListParams) ([]*ManagedInfo, int64, error) {
	base := h.projects.db.DB.Model(&ManagedRow{}).Where("id LIKE ?", prefix+"%")
	if len(clients) > 0 {
		var named []string
		unknown := false
		for _, c := range clients {
			if c == ClientUnknown {
				unknown = true
				continue
			}
			named = append(named, c)
		}
		switch {
		case unknown && len(named) > 0:
			base = base.Where("(client IN ? OR client = '' OR client IS NULL)", named)
		case unknown:
			base = base.Where("(client = '' OR client IS NULL)")
		default:
			base = base.Where("client IN ?", named)
		}
	}
	hide, only := h.host.ContentFilter()
	switch {
	case only:
		base = base.Where("id IN (?)", h.projects.db.DB.Model(&ProjectRow{}).Select("id").Where("adult = ?", false))
	case hide:
		base = base.Where("id IN (?)", h.projects.db.DB.Model(&ProjectRow{}).Select("id").Where("adult IS NULL OR adult = ?", false))
	}
	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	q := store.Order(base, params, ManagedListSpec).Order("id DESC")
	if params.Limit > 0 {
		q = q.Limit(params.Limit)
	}
	if params.Offset > 0 {
		q = q.Offset(params.Offset)
	}
	var rows []ManagedRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return infosOf(rows), total, nil
}

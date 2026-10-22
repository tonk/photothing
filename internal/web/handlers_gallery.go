// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Ton Kersten

package web

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"photothing/internal/auth"
	"photothing/internal/gallery"
	"photothing/internal/store"
)

const maxTags = 20

// albumURL is the URL of page n of an album listing ("" = root).
func albumURL(dir string, n int) string {
	u := (&url.URL{Path: "/"}).String()
	if dir != "" {
		u = (&url.URL{Path: "/a/" + dir}).EscapedPath()
	}
	if n > 1 {
		u += "?page=" + strconv.Itoa(n)
	}
	return u
}

// denyAlbum answers for an album the visitor may not see. Anonymous visitors are sent to the sign-in
// page for both restricted and non-existent albums, so the two cannot be told apart.
func (s *Server) denyAlbum(w http.ResponseWriter, r *http.Request) {
	if s.current(r) == nil {
		s.redirectToLogin(w, r)
		return
	}
	s.fail(w, r, http.StatusNotFound, "Album not found.")
}

// albumVisible reports whether dir contains at least one photo the user may see.
func (s *Server) albumVisible(dir string, allow func(string) bool) (bool, error) {
	if !allow(dir) {
		return false, nil
	}
	stats, err := s.store.DirStats()
	if err != nil {
		return false, err
	}
	for _, d := range stats {
		if (d.Dir == dir || strings.HasPrefix(d.Dir, dir+"/")) && allow(d.Dir) {
			return true, nil
		}
	}
	return false, nil
}

type albumData struct {
	Dir       string
	Crumbs    []crumb
	SubAlbums []store.SubAlbum
	Photos    []store.Photo
	Pager     pagerBox
}

func (s *Server) handleAlbum(w http.ResponseWriter, r *http.Request) {
	dir := path.Clean("/" + r.PathValue("path"))[1:] // "" for the root
	u := s.current(r)
	acc, err := s.loadAccess()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	allow := acc.filter(u)
	if dir != "" {
		ok, err := s.albumVisible(dir, allow)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			s.denyAlbum(w, r)
			return
		}
	}
	size, mode := prefs(u)
	refs, err := s.store.DirRefs(dir)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sortRefs(refs, mode)
	pg := newPager(len(refs), size, pageParam(r), albumURL(dir, 1)+"?")
	from, to := pg.window()
	photos, err := s.store.PhotosByIDs(ids(refs[from:to]))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var subs []store.SubAlbum
	if pg.Page == 1 {
		if subs, err = s.store.SubAlbums(dir, allow); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	title := s.T(r, "Albums")
	if dir != "" {
		title = path.Base(dir)
	}
	s.render(w, r, "album", page{Title: title, Data: albumData{Dir: dir, Crumbs: crumbs(dir), SubAlbums: subs, Photos: photos, Pager: s.pagerFor(r, pg, size)}})
}

// ---- single photo ----

// coverOpt is one album (the photo's directory or an ancestor) the photo can be made the cover of.
type coverOpt struct {
	Path     string
	IsCover  bool // this photo is already the cover of that album
	Selected bool // preselected in the form: the current cover, else the photo's own album
}

type photoData struct {
	Photo     *store.Photo
	Crumbs    []crumb
	Prev      int64
	Next      int64
	Index     int // 1-based position within the album
	Total     int
	UpURL     string
	Info      []infoRow
	Tags      []string
	TagsText  string
	CanOrig   bool
	CanEdit   bool
	CoverOpts []coverOpt
}

// infoRow is one line of photo information, ready to show: label and value in the user's language.
type infoRow struct{ Label, Value, Link string }

// infoRows orders and translates the stored photo information. Fields that reveal the location are
// left out for users who may not download originals. Fields this version does not know (stored by an
// older or newer version) are listed last, by name.
func (s *Server) infoRows(r *http.Request, p *store.Photo, mayDownload bool) []infoRow {
	valueTexts := gallery.ExifValueTexts()
	row := func(key string) (infoRow, bool) {
		v, ok := p.Exif[key]
		if !ok || (gallery.ExifSensitive[key] && !mayDownload) {
			return infoRow{}, false
		}
		out := infoRow{Label: s.T(r, key), Value: v}
		if _, translated := valueTexts[key]; translated {
			out.Value = s.T(r, v)
		}
		if key == gallery.LabelGPS {
			out.Link = osmURL(v)
		}
		return out, true
	}
	known := map[string]bool{}
	var rows []infoRow
	for _, key := range gallery.ExifOrder {
		known[key] = true
		if ir, ok := row(key); ok {
			rows = append(rows, ir)
		}
	}
	var extra []string
	for key := range p.Exif {
		if !known[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	for _, key := range extra {
		if ir, ok := row(key); ok {
			rows = append(rows, ir)
		}
	}
	return rows
}

// loadPhoto fetches the photo of the request and enforces album access.
func (s *Server) loadPhoto(w http.ResponseWriter, r *http.Request) *store.Photo {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Photo not found.")
		return nil
	}
	p, err := s.store.PhotoByID(id)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "Photo not found.")
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	acc, err := s.loadAccess()
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	if !acc.allowed(s.current(r), p.Dir) {
		if s.current(r) == nil {
			s.redirectToLogin(w, r)
		} else {
			s.fail(w, r, http.StatusNotFound, "Photo not found.")
		}
		return nil
	}
	return p
}

func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p := s.loadPhoto(w, r)
	if p == nil {
		return
	}
	s.renderPhoto(w, r, p, page{})
}

func (s *Server) renderPhoto(w http.ResponseWriter, r *http.Request, p *store.Photo, pg page) {
	u := s.current(r)
	size, mode := prefs(u)
	refs, err := s.store.DirRefs(p.Dir)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sortRefs(refs, mode)
	d := photoData{Photo: p, Crumbs: crumbs(p.Dir), Total: len(refs)}
	for i, ref := range refs {
		if ref.ID == p.ID {
			d.Index = i + 1
			if i > 0 {
				d.Prev = refs[i-1].ID
			}
			if i < len(refs)-1 {
				d.Next = refs[i+1].ID
			}
		}
	}
	d.UpURL = albumURL(p.Dir, (max(d.Index, 1)-1)/size+1)
	d.CanOrig = u != nil && auth.Can(u.Role, auth.PermOriginal)
	d.CanEdit = u != nil && auth.Can(u.Role, auth.PermEdit)

	d.Info = s.infoRows(r, p, d.CanOrig)
	if d.Tags, err = s.store.PhotoTags(p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.TagsText = strings.Join(d.Tags, ", ")

	if d.CanEdit {
		covers, err := s.store.Covers()
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, c := range d.Crumbs {
			d.CoverOpts = append(d.CoverOpts, coverOpt{Path: c.Path, IsCover: covers[c.Path].PhotoID == p.ID})
		}
		sel := len(d.CoverOpts) - 1 // the photo's own album
		for i, o := range d.CoverOpts {
			if o.IsCover {
				sel = i
				break
			}
		}
		if sel >= 0 {
			d.CoverOpts[sel].Selected = true
		}
	}
	title := p.Filename
	if p.Caption != "" {
		title = p.Caption
	}
	pg.Title, pg.Data = title, d
	if pg.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, r, "photo", pg)
}

// handleCover sets or resets the cover of one of the photo's albums.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	p := s.loadPhoto(w, r)
	if p == nil {
		return
	}
	album := r.PostFormValue("album")
	if album == "" || (p.Dir != album && !strings.HasPrefix(p.Dir, album+"/")) {
		s.renderPhoto(w, r, p, page{Error: s.T(r, "Choose one of this photo's albums.")})
		return
	}
	var err error
	var msg string
	if r.PostFormValue("action") == "reset" {
		err, msg = s.store.ResetCover(album), s.T(r, "Cover of %s reset to automatic.", path.Base(album))
		s.audit(r, "cover.reset", album, p.Path)
	} else {
		err, msg = s.store.SetCover(album, p.ID), s.T(r, "This photo is now the cover of %s.", path.Base(album))
		s.audit(r, "cover.set", album, p.Path)
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPhoto(w, r, p, page{Flash: msg})
}

// handleMeta stores the caption and tags of a photo.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	r.ParseForm()
	p := s.loadPhoto(w, r)
	if p == nil {
		return
	}
	caption := cleanText(r.PostFormValue("caption"), 300)
	seen := map[string]bool{}
	var tags []string
	for _, t := range strings.Split(r.PostFormValue("tags"), ",") {
		t = strings.TrimLeft(cleanText(t, 40), "#")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	if len(tags) > maxTags {
		s.renderPhoto(w, r, p, page{Error: s.T(r, "Too many tags (maximum %d).", maxTags)})
		return
	}
	sort.Strings(tags)
	if err := s.store.SetCaption(p.ID, caption); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetTags(p.ID, tags); err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Caption = caption
	s.audit(r, "photo.edit", p.Path, "")
	s.renderPhoto(w, r, p, page{Flash: s.T(r, "Details saved.")})
}

// ---- images and video ----

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	p := s.loadPhoto(w, r)
	if p == nil {
		return
	}
	kind := r.PathValue("kind")
	// Photos are behind authentication unless public mode is on, so never let shared caches store them.
	w.Header().Set("Cache-Control", "private, max-age=86400")

	switch kind {
	case "original", "video":
		u := s.current(r)
		if kind == "video" && !p.IsVideo() {
			s.fail(w, r, http.StatusNotFound, "Unknown image size.")
			return
		}
		// Videos are streamed to viewers (a browser has to download them to play them); other
		// originals need the download permission.
		if kind == "original" && (u == nil || !auth.Can(u.Role, auth.PermOriginal)) {
			s.fail(w, r, http.StatusForbidden, "You do not have permission to download originals.")
			return
		}
		f, err := s.lib.OpenOriginal(p.Path)
		if err != nil {
			s.fail(w, r, http.StatusNotFound, "File not found.")
			return
		}
		defer f.Close()
		ctype, inline := gallery.MimeFor(p.Filename)
		disp := "attachment"
		if inline {
			disp = "inline"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": p.Filename}))
		http.ServeContent(w, r, p.Filename, p.TakenAt, f)
	case "thumb", "large":
		out, err := s.lib.Variant(r.Context(), p, kind)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		http.ServeFile(w, r, out)
	default:
		s.fail(w, r, http.StatusNotFound, "Unknown image size.")
	}
}

// ---- search and tags ----

type searchData struct {
	Q, Tag   string
	Searched bool
	Tags     []store.TagCount
	Photos   []store.Photo
	Pager    pagerBox
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	q := cleanText(r.URL.Query().Get("q"), 100)
	tag := cleanText(r.URL.Query().Get("tag"), 40)
	acc, err := s.loadAccess()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	allow := acc.filter(u)
	d := searchData{Q: q, Tag: tag, Searched: q != "" || tag != ""}
	if !d.Searched {
		if d.Tags, err = s.store.TagCounts(allow); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, "search", page{Title: s.T(r, "Search"), Data: d})
		return
	}
	refs, err := s.store.Search(q, tag)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	visible := refs[:0]
	for _, ref := range refs {
		if allow(ref.Dir) {
			visible = append(visible, ref)
		}
	}
	size, mode := prefs(u)
	sortRefs(visible, mode)
	base := "/search?" + url.Values{"q": {q}, "tag": {tag}}.Encode() + "&"
	pg := newPager(len(visible), size, pageParam(r), base)
	from, to := pg.window()
	if d.Photos, err = s.store.PhotosByIDs(ids(visible[from:to])); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Pager = s.pagerFor(r, pg, size)
	s.render(w, r, "search", page{Title: s.T(r, "Search"), Data: d})
}

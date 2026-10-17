package dofake

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/digitalocean/godo"
)

// routes registers every endpoint the fake models. Paths and query
// parameters are the ones godo v1.205.0 sends; everything else falls
// through to unknown.
func (s *Server) routes() {
	for pattern, h := range map[string]http.HandlerFunc{
		"POST /v2/droplets":                   s.createDroplet,
		"GET /v2/droplets":                    s.listDroplets,
		"GET /v2/droplets/{id}":               s.getDroplet,
		"DELETE /v2/droplets/{id}":            s.deleteDroplet,
		"POST /v2/droplets/{id}/actions":      s.dropletAction,
		"GET /v2/droplets/{id}/actions/{aid}": s.getDropletAction,
		"GET /v2/actions/{aid}":               s.getAction,
		"GET /v2/images":                      s.listImages,
		"GET /v2/images/{id}":                 s.getImage,
		"PUT /v2/images/{id}":                 s.updateImage,
		"DELETE /v2/images/{id}":              s.deleteImage,
		"GET /v2/account/keys":                s.listKeys,
		"POST /v2/account/keys":               s.createKey,
		"GET /v2/account/keys/{id}":           s.getKey,
		"DELETE /v2/account/keys/{id}":        s.deleteKey,
		"POST /v2/tags":                       s.createTag,
		"GET /v2/tags/{name}":                 s.getTag,
		"POST /v2/tags/{name}/resources":      s.tagResources,
		"GET /v2/sizes":                       s.listSizes,
		"GET /v2/regions":                     s.listRegions,
		"GET /v2/account":                     s.getAccount,
	} {
		s.mux.Handle(pattern, authenticated(h))
	}
	// Authenticated, as the real API is, and for a second reason: other
	// processes on the machine probe fresh localhost listeners (two bare
	// GET / requests each, without a token) and would otherwise land in
	// the unknown list and fail whichever test owned the port. The
	// provider always sends a token, so this filters nothing it sends.
	s.mux.Handle("/", authenticated(s.unknownRoute))
}

// authenticated refuses a request without a bearer token, as the real
// API does, so a client that lost its token fails here and not first on
// a paid run.
func authenticated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok || tok == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Unable to authenticate you")
			return
		}
		h(w, r)
	}
}

func (s *Server) unknownRoute(w http.ResponseWriter, r *http.Request) {
	what := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		what += "?" + r.URL.RawQuery
	}
	s.mu.Lock()
	s.unknown = append(s.unknown, what)
	s.mu.Unlock()
	// 501 rather than 404: a 404 is an answer the code under test may
	// branch on, and godo does not retry a 501.
	writeError(w, http.StatusNotImplemented, "not_implemented", "dofake does not model "+what)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes DigitalOcean's error body, which godo decodes into
// ErrorResponse.
func writeError(w http.ResponseWriter, status int, id, message string) {
	writeJSON(w, status, map[string]string{"id": id, "message": message})
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "The resource you were accessing could not be found.")
}

func unprocessable(w http.ResponseWriter, format string, a ...any) {
	writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity", fmt.Sprintf(format, a...))
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(r *http.Request, name string) (int, bool) {
	n, err := strconv.Atoi(r.PathValue(name))
	return n, err == nil && n > 0
}

// paging reads the request's page and per_page, with DigitalOcean's
// default of 20 and cap of 200, and returns the bounds of that page in
// a list of n items.
func paging(r *http.Request, n int) (page, perPage, start, end int) {
	q := r.URL.Query()
	perPage, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || perPage < 1 {
		perPage = 20
	}
	perPage = min(perPage, 200)
	page, err = strconv.Atoi(q.Get("page"))
	if err != nil || page < 1 {
		page = 1
	}
	start = min(n, (page-1)*perPage)
	return page, perPage, start, min(n, start+perPage)
}

func writePage[T any](w http.ResponseWriter, r *http.Request, key string, items []T) {
	page, perPage, start, end := paging(r, len(items))
	writeEnvelope(w, r, key, items[start:end], len(items), page, perPage)
}

// writeEnvelope writes one page of a list under key with DigitalOcean's
// links and meta. prev and first are included past page one because
// godo works out the current page from prev, and a client paginating
// without them would ask for page one again.
func writeEnvelope[T any](w http.ResponseWriter, r *http.Request, key string, items []T, total, page, perPage int) {
	out := slices.Clone(items)
	if out == nil {
		out = []T{}
	}
	pages := max(1, (total+perPage-1)/perPage)
	link := func(p int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p))
		q.Set("per_page", strconv.Itoa(perPage))
		return (&url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawQuery: q.Encode()}).String()
	}
	links := godo.Links{}
	if pages > 1 {
		links.Pages = &godo.Pages{}
		if page > 1 {
			links.Pages.First = link(1)
			links.Pages.Prev = link(page - 1)
		}
		if page < pages {
			links.Pages.Next = link(page + 1)
			links.Pages.Last = link(pages)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		key:     out,
		"links": links,
		"meta":  godo.Meta{Total: total},
	})
}

// --- droplets ---

// createRequest is godo.DropletCreateRequest as it arrives: image and
// ssh_keys are each a number or a string, which godo marshals but does
// not unmarshal.
type createRequest struct {
	Name     string            `json:"name"`
	Region   string            `json:"region"`
	Size     string            `json:"size"`
	Image    json.RawMessage   `json:"image"`
	SSHKeys  []json.RawMessage `json:"ssh_keys"`
	UserData string            `json:"user_data"`
	Tags     []string          `json:"tags"`
}

// idOrString decodes a value godo sends as either a number or a string.
func idOrString(raw json.RawMessage) (int, string, error) {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, "", nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err != nil {
		return 0, "", fmt.Errorf("want a number or a string, got %s", raw)
	}
	return 0, str, nil
}

func (s *Server) createDroplet(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !decode(w, r, &req) {
		return
	}

	s.mu.Lock()
	d, keys, problem := s.validateCreate(&req)
	if problem != "" {
		s.mu.Unlock()
		unprocessable(w, "%s", problem)
		return
	}
	d.ID = s.allocate("droplet")
	d.Created = s.now()
	backend := s.backend
	s.mu.Unlock()

	ip, err := backend.Create(d, req.UserData, keys)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "creating droplet: "+err.Error())
		return
	}
	// Active with an address straight away: the provider polls until it
	// sees both, and there is nothing for a fake to wait for.
	d.Status = "active"
	d.Networks = &godo.Networks{V4: []godo.NetworkV4{
		{IPAddress: ip, Netmask: "255.255.255.0", Gateway: gateway(ip), Type: "public"},
	}}

	s.mu.Lock()
	s.droplets[d.ID] = &dropletRecord{droplet: d, userData: req.UserData}
	a := s.recordAction(d.ID, "create", regionSlug(&d))
	s.mu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"droplet": d,
		"links":   godo.Links{Actions: []godo.LinkAction{{ID: a.ID, Rel: "create", HREF: s.URL() + "/v2/actions/" + strconv.Itoa(a.ID)}}},
	})
}

// validateCreate checks a create as DigitalOcean does before it bills
// anything, and builds the droplet it describes. Callers hold s.mu.
func (s *Server) validateCreate(req *createRequest) (godo.Droplet, []godo.Key, string) {
	if req.Name == "" || req.Region == "" || req.Size == "" || len(req.Image) == 0 {
		return godo.Droplet{}, nil, "name, region, size and image are required"
	}
	i := slices.IndexFunc(s.sizes, func(z godo.Size) bool { return z.Slug == req.Size })
	if i < 0 {
		return godo.Droplet{}, nil, fmt.Sprintf("You specified an invalid size for Droplet creation: %q.", req.Size)
	}
	size := s.sizes[i]
	if len(size.Regions) > 0 && !slices.Contains(size.Regions, req.Region) {
		return godo.Droplet{}, nil, fmt.Sprintf("Size %s is not available in region %s.", size.Slug, req.Region)
	}

	img, problem := s.createImage(req.Image, req.Region, size)
	if problem != "" {
		return godo.Droplet{}, nil, problem
	}

	keys := make([]godo.Key, 0, len(req.SSHKeys))
	for _, raw := range req.SSHKeys {
		k, ok := s.findKey(raw)
		if !ok {
			return godo.Droplet{}, nil, fmt.Sprintf("You specified an invalid SSH key: %s.", raw)
		}
		keys = append(keys, k)
	}

	sz := size
	return godo.Droplet{
		Name:      req.Name,
		Memory:    size.Memory,
		Vcpus:     size.Vcpus,
		Disk:      size.Disk,
		Region:    &godo.Region{Slug: req.Region, Name: req.Region, Available: true},
		Image:     &img,
		Size:      &sz,
		SizeSlug:  size.Slug,
		Status:    "new",
		Tags:      slices.Clone(req.Tags),
		VolumeIDs: []string{},
	}, keys, ""
}

// createImage resolves the create request's image: a number is one of
// the account's images and must be usable here; a string is a seeded
// image's slug or, failing that, a stock distribution image the fake
// takes on trust.
func (s *Server) createImage(raw json.RawMessage, region string, size godo.Size) (godo.Image, string) {
	id, slug, err := idOrString(raw)
	if err != nil {
		return godo.Image{}, "invalid image: " + err.Error()
	}
	if slug != "" {
		for _, img := range s.images {
			if img.Slug == slug {
				return s.usable(*img, region, size)
			}
		}
		return godo.Image{Slug: slug, Name: slug, Type: "base", Distribution: "Ubuntu", Public: true, Status: "available"}, ""
	}
	img, ok := s.images[id]
	if !ok {
		return godo.Image{}, fmt.Sprintf("You specified an invalid image for Droplet creation: %d.", id)
	}
	return s.usable(*img, region, size)
}

func (s *Server) usable(img godo.Image, region string, size godo.Size) (godo.Image, string) {
	switch {
	case img.Status != "available":
		return godo.Image{}, fmt.Sprintf("Image %d is not available (status %q).", img.ID, img.Status)
	case len(img.Regions) > 0 && !slices.Contains(img.Regions, region):
		return godo.Image{}, fmt.Sprintf("Image %d is not available in region %s.", img.ID, region)
	case img.MinDiskSize > size.Disk:
		return godo.Image{}, fmt.Sprintf("Image %d needs a %d GB disk; size %s has %d GB.", img.ID, img.MinDiskSize, size.Slug, size.Disk)
	}
	return copyImage(img), ""
}

// findKey resolves an ssh_keys entry, an ID or a fingerprint. Callers
// hold s.mu.
func (s *Server) findKey(raw json.RawMessage) (godo.Key, bool) {
	id, fp, err := idOrString(raw)
	if err != nil {
		return godo.Key{}, false
	}
	return s.lookupKey(id, fp)
}

func (s *Server) lookupKey(id int, fp string) (godo.Key, bool) {
	if fp == "" {
		k, ok := s.keys[id]
		if !ok {
			return godo.Key{}, false
		}
		return *k, true
	}
	for _, k := range s.keys {
		if k.Fingerprint == fp {
			return *k, true
		}
	}
	return godo.Key{}, false
}

func gateway(ip string) string {
	if i := strings.LastIndex(ip, "."); i > 0 {
		return ip[:i] + ".1"
	}
	return ""
}

func (s *Server) listDroplets(w http.ResponseWriter, r *http.Request) {
	tag, name := r.URL.Query().Get("tag_name"), r.URL.Query().Get("name")
	s.mu.Lock()
	var out []godo.Droplet
	for _, id := range sortedKeys(s.droplets) {
		d := s.droplets[id].droplet
		if (tag == "" || slices.Contains(d.Tags, tag)) && (name == "" || d.Name == name) {
			out = append(out, d)
		}
	}
	s.mu.Unlock()
	writePage(w, r, "droplets", out)
}

func (s *Server) getDroplet(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	s.mu.Lock()
	rec, ok := s.droplets[id]
	var d godo.Droplet
	if ok {
		d = rec.droplet
	}
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"droplet": d})
}

func (s *Server) deleteDroplet(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	s.mu.Lock()
	_, ok := s.droplets[id]
	backend := s.backend
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	if err := backend.Destroy(id); err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "destroying droplet: "+err.Error())
		return
	}
	s.mu.Lock()
	delete(s.droplets, id)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// recordAction stores a completed action. Callers hold s.mu.
func (s *Server) recordAction(resourceID int, kind, region string) godo.Action {
	at := &godo.Timestamp{Time: s.clock}
	a := godo.Action{
		ID:           s.allocate("action"),
		Status:       godo.ActionCompleted,
		Type:         kind,
		StartedAt:    at,
		CompletedAt:  at,
		ResourceID:   resourceID,
		ResourceType: "droplet",
		RegionSlug:   region,
	}
	s.actions[a.ID] = &a
	return a
}

// dropletAction handles the actions the Packer plugin takes on its
// builder. Each completes before the response, so an action poll always
// reads completed.
func (s *Server) dropletAction(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	var req struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	rec, ok := s.droplets[id]
	backend := s.backend
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	region := regionSlug(&rec.droplet)

	switch req.Type {
	case "power_off", "shutdown":
		if err := backend.PowerOff(id); err != nil {
			writeError(w, http.StatusInternalServerError, "server_error", req.Type+": "+err.Error())
			return
		}
		s.setStatus(id, "off")
	case "power_on":
		s.setStatus(id, "active")
	case "snapshot":
		if req.Name == "" {
			unprocessable(w, "snapshot needs a name")
			return
		}
		if _, err := s.Snapshot(id, req.Name); err != nil {
			if errors.Is(err, errNoDroplet) {
				notFound(w)
				return
			}
			writeError(w, http.StatusInternalServerError, "server_error", err.Error())
			return
		}
	default:
		unprocessable(w, "dofake does not model droplet action %q", req.Type)
		return
	}

	s.mu.Lock()
	a := s.recordAction(id, req.Type, region)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"action": a})
}

func (s *Server) setStatus(id int, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.droplets[id]; ok {
		rec.droplet.Status = status
	}
}

func (s *Server) getDropletAction(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	s.writeAction(w, r, func(a *godo.Action) bool { return a.ResourceID == id })
}

func (s *Server) getAction(w http.ResponseWriter, r *http.Request) {
	s.writeAction(w, r, func(*godo.Action) bool { return true })
}

func (s *Server) writeAction(w http.ResponseWriter, r *http.Request, belongs func(*godo.Action) bool) {
	aid, _ := pathID(r, "aid")
	s.mu.Lock()
	a, ok := s.actions[aid]
	var out godo.Action
	if ok && belongs(a) {
		out = *a
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": out})
}

// --- images ---

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	private, tag, kind := q.Get("private") == "true", q.Get("tag_name"), q.Get("type")
	s.mu.Lock()
	var ids []int
	for _, id := range sortedKeys(s.images) {
		img := s.images[id]
		if (!private || !img.Public) && (tag == "" || slices.Contains(img.Tags, tag)) && (kind == "" || img.Type == kind) {
			ids = append(ids, id)
		}
	}
	// Only the images on the page returned count as read.
	page, perPage, start, end := paging(r, len(ids))
	out := make([]godo.Image, 0, end-start)
	for _, id := range ids[start:end] {
		out = append(out, s.readImage(id))
	}
	s.mu.Unlock()
	writeEnvelope(w, r, "images", out, len(ids), page, perPage)
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	id, ok := s.imageID(r.PathValue("id"))
	var img godo.Image
	if ok {
		img = s.readImage(id)
	}
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"image": img})
}

// imageID resolves a path segment that godo fills with an ID or, for
// GetBySlug, a slug. Callers hold s.mu.
func (s *Server) imageID(seg string) (int, bool) {
	if n, err := strconv.Atoi(seg); err == nil {
		_, ok := s.images[n]
		return n, ok
	}
	for id, img := range s.images {
		if img.Slug != "" && img.Slug == seg {
			return id, true
		}
	}
	return 0, false
}

func (s *Server) updateImage(w http.ResponseWriter, r *http.Request) {
	var req godo.ImageUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	id, _ := pathID(r, "id")
	s.mu.Lock()
	img, ok := s.images[id]
	var out godo.Image
	if ok {
		if req.Name != "" {
			img.Name = req.Name
		}
		if req.Distribution != "" {
			img.Distribution = req.Distribution
		}
		if req.Description != "" {
			img.Description = req.Description
		}
		out = copyImage(*img)
	}
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"image": out})
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	id, _ := pathID(r, "id")
	if !s.RemoveImage(id) {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- keys ---

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	writePage(w, r, "ssh_keys", s.Keys())
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var req godo.KeyCreateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		unprocessable(w, "Name can't be blank")
		return
	}
	fp, err := fingerprint(req.PublicKey)
	if err != nil {
		unprocessable(w, "Key invalid: %v", err)
		return
	}
	s.mu.Lock()
	if _, taken := s.lookupKey(0, fp); taken {
		s.mu.Unlock()
		unprocessable(w, "SSH Key is already in use on your account")
		return
	}
	k := godo.Key{ID: s.allocate("key"), Name: req.Name, Fingerprint: fp, PublicKey: req.PublicKey}
	s.keys[k.ID] = &k
	out := k
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"ssh_key": out})
}

// keyFromPath resolves the {id} segment, which godo fills with an ID or
// a fingerprint. Callers hold s.mu.
func (s *Server) keyFromPath(r *http.Request) (godo.Key, bool) {
	seg := r.PathValue("id")
	if n, err := strconv.Atoi(seg); err == nil {
		return s.lookupKey(n, "")
	}
	return s.lookupKey(0, seg)
}

func (s *Server) getKey(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	k, ok := s.keyFromPath(r)
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ssh_key": k})
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	k, ok := s.keyFromPath(r)
	if ok {
		delete(s.keys, k.ID)
	}
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- tags ---

// createTag is idempotent, which is what the provider's TagImage relies
// on when it creates the tag before attaching it.
func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	var req godo.TagCreateRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		unprocessable(w, "name can't be blank")
		return
	}
	s.mu.Lock()
	s.tags[req.Name] = true
	tag := s.tag(req.Name)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"tag": tag})
}

// tag builds a tag with its resource counts. Callers hold s.mu.
func (s *Server) tag(name string) godo.Tag {
	var images, droplets int
	for _, img := range s.images {
		if slices.Contains(img.Tags, name) {
			images++
		}
	}
	for _, rec := range s.droplets {
		if slices.Contains(rec.droplet.Tags, name) {
			droplets++
		}
	}
	return godo.Tag{Name: name, Resources: &godo.TaggedResources{
		Count:           images + droplets,
		Droplets:        &godo.TaggedDropletsResources{Count: droplets},
		Images:          &godo.TaggedImagesResources{Count: images},
		Volumes:         &godo.TaggedVolumesResources{},
		VolumeSnapshots: &godo.TaggedVolumeSnapshotsResources{},
		Databases:       &godo.TaggedDatabasesResources{},
	}}
}

func (s *Server) getTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	ok := s.tags[name]
	tag := s.tag(name)
	s.mu.Unlock()
	if !ok {
		notFound(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tag": tag})
}

// tagResources attaches a tag to images and droplets. It checks every
// resource before changing any, and answers 404 for a missing tag or
// resource.
func (s *Server) tagResources(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req godo.TagResourcesRequest
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tags[name] {
		notFound(w)
		return
	}
	var apply []func()
	for _, res := range req.Resources {
		id, err := strconv.Atoi(res.ID)
		switch {
		case err != nil:
			unprocessable(w, "invalid resource id %q", res.ID)
			return
		case res.Type == godo.ImageResourceType:
			img, ok := s.images[id]
			if !ok {
				notFound(w)
				return
			}
			apply = append(apply, func() { img.Tags = addTag(img.Tags, name) })
		case res.Type == godo.DropletResourceType:
			rec, ok := s.droplets[id]
			if !ok {
				notFound(w)
				return
			}
			apply = append(apply, func() { rec.droplet.Tags = addTag(rec.droplet.Tags, name) })
		default:
			unprocessable(w, "dofake does not model tagging %q resources", res.Type)
			return
		}
	}
	for _, f := range apply {
		f()
	}
	w.WriteHeader(http.StatusNoContent)
}

func addTag(tags []string, tag string) []string {
	if slices.Contains(tags, tag) {
		return tags
	}
	return append(tags, tag)
}

// --- sizes, regions, account ---

func (s *Server) listSizes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sizes := slices.Clone(s.sizes)
	s.mu.Unlock()
	writePage(w, r, "sizes", sizes)
}

// listRegions derives the regions from the seeded sizes: a region with
// no size to offer would be no use to a create anyway.
func (s *Server) listRegions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	bySlug := map[string]*godo.Region{}
	var order []string
	for _, z := range s.sizes {
		for _, slug := range z.Regions {
			reg, ok := bySlug[slug]
			if !ok {
				reg = &godo.Region{Slug: slug, Name: slug, Available: true}
				bySlug[slug] = reg
				order = append(order, slug)
			}
			reg.Sizes = append(reg.Sizes, z.Slug)
		}
	}
	s.mu.Unlock()
	slices.Sort(order)
	regions := make([]godo.Region, 0, len(order))
	for _, slug := range order {
		regions = append(regions, *bySlug[slug])
	}
	writePage(w, r, "regions", regions)
}

func (s *Server) getAccount(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"account": godo.Account{
		DropletLimit:  25,
		Email:         "dofake@example.invalid",
		UUID:          "dofake-account",
		EmailVerified: true,
		Status:        "active",
	}})
}

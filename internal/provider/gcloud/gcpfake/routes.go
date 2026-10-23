package gcpfake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var nextID uint64 = 1000

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/compute/v1/projects/", s.handleProjects)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	// Path format: /compute/v1/projects/{project}/...
	path := strings.TrimPrefix(r.URL.Path, "/compute/v1/projects/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}

	project := parts[0]
	scope := parts[1] // "zones" or "global"

	switch scope {
	case "zones":
		if len(parts) < 4 {
			http.NotFound(w, r)
			return
		}
		zone := parts[2]
		resource := parts[3]
		var name string
		if len(parts) >= 5 {
			name = parts[4]
		}
		s.handleZoneResource(w, r, project, zone, resource, name)
	case "global":
		if len(parts) < 3 {
			http.NotFound(w, r)
			return
		}
		resource := parts[2]
		var name string
		if len(parts) >= 4 {
			name = parts[3]
		}
		var subaction string
		if len(parts) >= 5 {
			subaction = parts[4]
		}
		s.handleGlobalResource(w, r, project, resource, name, subaction)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleZoneResource(w http.ResponseWriter, r *http.Request, project, zone, resource, name string) {
	switch resource {
	case "instances":
		if name == "" {
			switch r.Method {
			case http.MethodPost:
				s.insertInstance(w, r, project, zone)
			case http.MethodGet:
				s.listInstances(w, project, zone)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.getInstance(w, project, zone, name)
		case http.MethodDelete:
			s.deleteInstance(w, project, zone, name)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "operations":
		s.getZoneOperation(w, project, zone, name)
	case "disks":
		if name == "" {
			switch r.Method {
			case http.MethodPost:
				s.insertDisk(w, r, project, zone)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.getDisk(w, project, zone, name)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleGlobalResource(w http.ResponseWriter, r *http.Request, project, resource, name, subaction string) {
	switch resource {
	case "operations":
		s.getGlobalOperation(w, project, name)
	case "images":
		if subaction == "setLabels" && r.Method == http.MethodPost {
			s.setImageLabels(w, r, project, name)
			return
		}
		if name == "" {
			switch r.Method {
			case http.MethodPost:
				s.insertImage(w, r, project)
			case http.MethodGet:
				s.listImages(w, project)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.getImage(w, project, name)
		case http.MethodDelete:
			s.deleteImage(w, project, name)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) insertInstance(w http.ResponseWriter, r *http.Request, project, zone string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var inst computepb.Instance
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, &inst); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := atomic.AddUint64(&nextID, 1)
	inst.Id = ptr(id)
	inst.Status = ptr("RUNNING")
	inst.Zone = ptr(fmt.Sprintf("%s/compute/v1/projects/%s/zones/%s", s.srv.URL, project, zone))
	inst.SelfLink = ptr(fmt.Sprintf("%s/compute/v1/projects/%s/zones/%s/instances/%s", s.srv.URL, project, zone, inst.GetName()))

	if len(inst.NetworkInterfaces) == 0 {
		inst.NetworkInterfaces = []*computepb.NetworkInterface{
			{
				NetworkIP: ptr("10.128.0.2"),
			},
		}
	} else if inst.NetworkInterfaces[0].NetworkIP == nil {
		inst.NetworkInterfaces[0].NetworkIP = ptr("10.128.0.2")
	}

	key := fmt.Sprintf("%s/%s/%s", project, zone, inst.GetName())
	s.instances[key] = &inst

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:          ptr(opName),
		Status:        computepb.Operation_DONE.Enum(),
		TargetLink:    inst.SelfLink,
		SelfLink:      ptr(fmt.Sprintf("%s/compute/v1/projects/%s/zones/%s/operations/%s", s.srv.URL, project, zone, opName)),
		OperationType: ptr("insert"),
		Progress:      ptr(int32(100)),
		Zone:          inst.Zone,
	}
	s.operations[fmt.Sprintf("%s/%s/%s", project, zone, opName)] = op

	writeProto(w, op)
}

func (s *Server) getInstance(w http.ResponseWriter, project, zone, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%s", project, zone, name)
	inst, ok := s.instances[key]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("The resource 'projects/%s/zones/%s/instances/%s' was not found", project, zone, name))
		return
	}
	writeProto(w, inst)
}

func (s *Server) listInstances(w http.ResponseWriter, project, zone string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix := fmt.Sprintf("%s/%s/", project, zone)
	var items []*computepb.Instance
	for k, v := range s.instances {
		if strings.HasPrefix(k, prefix) {
			items = append(items, v)
		}
	}

	resp := &computepb.InstanceList{
		Items: items,
		Id:    ptr(fmt.Sprintf("projects/%s/zones/%s/instances", project, zone)),
	}
	writeProto(w, resp)
}

func (s *Server) deleteInstance(w http.ResponseWriter, project, zone, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%s", project, zone, name)
	delete(s.instances, key)

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:          ptr(opName),
		Status:        computepb.Operation_DONE.Enum(),
		SelfLink:      ptr(fmt.Sprintf("%s/compute/v1/projects/%s/zones/%s/operations/%s", s.srv.URL, project, zone, opName)),
		OperationType: ptr("delete"),
		Progress:      ptr(int32(100)),
	}
	s.operations[fmt.Sprintf("%s/%s/%s", project, zone, opName)] = op
	writeProto(w, op)
}

func (s *Server) getZoneOperation(w http.ResponseWriter, project, zone, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%s", project, zone, name)
	op, ok := s.operations[key]
	if !ok {
		op = &computepb.Operation{
			Name:     ptr(name),
			Status:   computepb.Operation_DONE.Enum(),
			Progress: ptr(int32(100)),
		}
	}
	writeProto(w, op)
}

func (s *Server) getGlobalOperation(w http.ResponseWriter, project, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/global/%s", project, name)
	op, ok := s.operations[key]
	if !ok {
		op = &computepb.Operation{
			Name:     ptr(name),
			Status:   computepb.Operation_DONE.Enum(),
			Progress: ptr(int32(100)),
		}
	}
	writeProto(w, op)
}

func (s *Server) insertDisk(w http.ResponseWriter, r *http.Request, project, zone string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var disk computepb.Disk
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	_ = opts.Unmarshal(body, &disk)

	s.mu.Lock()
	defer s.mu.Unlock()

	disk.Status = ptr("READY")
	key := fmt.Sprintf("%s/%s/%s", project, zone, disk.GetName())
	s.disks[key] = &disk

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:     ptr(opName),
		Status:   computepb.Operation_DONE.Enum(),
		Progress: ptr(int32(100)),
	}
	writeProto(w, op)
}

func (s *Server) getDisk(w http.ResponseWriter, project, zone, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s/%s", project, zone, name)
	disk, ok := s.disks[key]
	if !ok {
		disk = &computepb.Disk{
			Name:     ptr(name),
			SizeGb:   ptr(int64(50)),
			Status:   ptr("READY"),
			SelfLink: ptr(fmt.Sprintf("%s/compute/v1/projects/%s/zones/%s/disks/%s", s.srv.URL, project, zone, name)),
		}
	}
	writeProto(w, disk)
}

func (s *Server) insertImage(w http.ResponseWriter, r *http.Request, project string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var img computepb.Image
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(body, &img); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	img.Status = ptr("READY")
	img.SelfLink = ptr(fmt.Sprintf("%s/compute/v1/projects/%s/global/images/%s", s.srv.URL, project, img.GetName()))
	key := fmt.Sprintf("%s/%s", project, img.GetName())
	s.images[key] = &img

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:     ptr(opName),
		Status:   computepb.Operation_DONE.Enum(),
		Progress: ptr(int32(100)),
	}
	writeProto(w, op)
}

func (s *Server) getImage(w http.ResponseWriter, project, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s", project, name)
	img, ok := s.images[key]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("The resource 'projects/%s/global/images/%s' was not found", project, name))
		return
	}
	writeProto(w, img)
}

func (s *Server) listImages(w http.ResponseWriter, project string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix := fmt.Sprintf("%s/", project)
	var items []*computepb.Image
	for k, v := range s.images {
		if strings.HasPrefix(k, prefix) {
			items = append(items, v)
		}
	}

	resp := &computepb.ImageList{
		Items: items,
		Id:    ptr(fmt.Sprintf("projects/%s/global/images", project)),
	}
	writeProto(w, resp)
}

func (s *Server) deleteImage(w http.ResponseWriter, project, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s", project, name)
	if _, ok := s.images[key]; !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("The resource 'projects/%s/global/images/%s' was not found", project, name))
		return
	}
	delete(s.images, key)

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:     ptr(opName),
		Status:   computepb.Operation_DONE.Enum(),
		Progress: ptr(int32(100)),
	}
	writeProto(w, op)
}

func (s *Server) setImageLabels(w http.ResponseWriter, r *http.Request, project, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := fmt.Sprintf("%s/%s", project, name)
	img, ok := s.images[key]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("The resource 'projects/%s/global/images/%s' was not found", project, name))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req computepb.GlobalSetLabelsRequest
	if err := protojson.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if img.Labels == nil {
		img.Labels = make(map[string]string)
	}
	for k, v := range req.Labels {
		img.Labels[k] = v
	}
	newFp := fmt.Sprintf("%d", time.Now().UnixNano())
	img.LabelFingerprint = &newFp

	opName := s.nextOpName()
	op := &computepb.Operation{
		Name:     ptr(opName),
		Status:   computepb.Operation_DONE.Enum(),
		Progress: ptr(int32(100)),
	}
	writeProto(w, op)
}

func writeProto(w http.ResponseWriter, m proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	opts := protojson.MarshalOptions{UseProtoNames: false}
	data, err := opts.Marshal(m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": msg,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

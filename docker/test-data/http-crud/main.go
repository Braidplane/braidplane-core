package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type item struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type store struct {
	mu     sync.Mutex
	nextID int
	items  map[int]item
}

func newStore() *store {
	return &store{
		nextID: 1,
		items:  make(map[int]item),
	}
}

func (s *store) create(it item) item {
	s.mu.Lock()
	defer s.mu.Unlock()

	it.ID = s.nextID
	s.nextID++
	s.items[it.ID] = it
	return it
}

func (s *store) update(id int, it item) (item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[id]; !ok {
		return item{}, false
	}

	it.ID = id
	s.items[id] = it
	return it, true
}

func (s *store) get(id int) (item, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	it, ok := s.items[id]
	return it, ok
}

func (s *store) delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[id]; !ok {
		return false
	}

	delete(s.items, id)
	return true
}

func (s *store) list(status string) []item {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]item, 0, len(s.items))
	for _, it := range s.items {
		if status != "" && it.Status != status {
			continue
		}
		items = append(items, it)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].ID < items[j].ID
	})

	return items
}

func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID = 1
	s.items = make(map[int]item)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func decodeBody(r *http.Request, target *item) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// writeMultipartResponse returns a deterministic multipart body for connector tests.
func writeMultipartResponse(w http.ResponseWriter) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("name", "Alice"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart field failed"})
		return
	}
	if err := writer.WriteField("tag", "urgent"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart field failed"})
		return
	}
	if err := writer.WriteField("tag", "customer"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart field failed"})
		return
	}

	filePart, err := writer.CreateFormFile("upload", "import.csv")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart file failed"})
		return
	}
	if _, err := filePart.Write([]byte("id\n1\n")); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart write failed"})
		return
	}
	if err := writer.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "multipart close failed"})
		return
	}

	w.Header().Set("Content-Type", writer.FormDataContentType())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

// writeMultipartEcho parses a multipart upload and echoes safe field and file details.
func writeMultipartEcho(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart form"})
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	files := make([]map[string]any, 0)
	if r.MultipartForm != nil {
		for name, headers := range r.MultipartForm.File {
			for _, header := range headers {
				file, err := header.Open()
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart file"})
					return
				}
				content, err := io.ReadAll(file)
				_ = file.Close()
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid multipart file"})
					return
				}
				files = append(files, map[string]any{
					"name":         name,
					"filename":     header.Filename,
					"content_type": header.Header.Get("Content-Type"),
					"size_bytes":   len(content),
					"content":      string(content),
				})
			}
		}
	}

	sort.Slice(files, func(i, j int) bool {
		leftName := files[i]["name"].(string)
		rightName := files[j]["name"].(string)
		if leftName == rightName {
			return files[i]["filename"].(string) < files[j]["filename"].(string)
		}
		return leftName < rightName
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true,
		"fields":   r.MultipartForm.Value,
		"files":    files,
	})
}

func main() {
	st := newStore()
	ackStore := newStore()
	var unstableHealthMu sync.Mutex
	unstableHealthOK := true

	http.HandleFunc("/acknowledgement", func(w http.ResponseWriter, r *http.Request) {
		for name, expected := range map[string]string{"X-Access-Key": "synthetic-access", "X-Secret-Key": "synthetic-secret", "X-App-Key": "synthetic-app"} {
			if r.Header.Get(name) != expected {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"status": map[string]string{"code": "DENIED"}})
				return
			}
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"status": map[string]string{"code": "OK"}, "records": ackStore.list(r.URL.Query().Get("status"))})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Query().Get("mode") {
		case "reject":
			writeJSON(w, http.StatusOK, map[string]any{"status": map[string]string{"code": "ERROR"}})
			return
		case "echo":
			writeJSON(w, http.StatusOK, map[string]any{"status": map[string]string{"code": "OK"}, "echo": r.Header.Get("X-Secret-Key")})
			return
		}
		var input item
		if decodeBody(r, &input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		created := ackStore.create(input)
		// Faults follow acceptance so clients must preserve an uncertain write outcome.
		switch r.URL.Query().Get("mode") {
		case "timeout":
			<-r.Context().Done()
			return
		case "missing":
			writeJSON(w, http.StatusOK, map[string]int{"id": created.ID})
		case "malformed":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":`))
		case "record-error":
			writeJSON(w, http.StatusOK, map[string]any{"status": map[string]string{"code": "OK"}, "records": []any{map[string]any{"id": created.ID, "errors": map[string]any{"error": map[string]string{"field": "name", "message": "synthetic validation error"}}}}})
		default:
			writeJSON(w, http.StatusCreated, map[string]any{"status": map[string]string{"code": "OK"}, "records": []any{map[string]any{"id": created.ID, "errors": []any{}}}})
		}
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	http.HandleFunc("/unstable-health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		unstableHealthMu.Lock()
		healthy := unstableHealthOK
		unstableHealthMu.Unlock()

		if !healthy {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	http.HandleFunc("/unstable-health/fail", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		unstableHealthMu.Lock()
		unstableHealthOK = false
		unstableHealthMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"unstable_health": "failed"})
	})

	http.HandleFunc("/unstable-health/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		unstableHealthMu.Lock()
		unstableHealthOK = true
		unstableHealthMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"unstable_health": "ok"})
	})

	http.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		st.reset()
		writeJSON(w, http.StatusOK, map[string]any{"reset": true})
	})

	http.HandleFunc("/multipart-response", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		writeMultipartResponse(w)
	})

	http.HandleFunc("/multipart-echo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}

		writeMultipartEcho(w, r)
	})

	http.HandleFunc("/items", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var input item
			if err := decodeBody(r, &input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
				return
			}

			if input.Name == "" || input.Status == "" {
				writeJSON(
					w,
					http.StatusBadRequest,
					map[string]any{"error": "name and status required"},
				)
				return
			}

			created := st.create(item{Name: input.Name, Status: input.Status})
			writeJSON(w, http.StatusCreated, created)
		case http.MethodGet:
			status := r.URL.Query().Get("status")
			items := st.list(status)
			writeJSON(w, http.StatusOK, items)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		}
	})

	http.HandleFunc("/items/", func(w http.ResponseWriter, r *http.Request) {
		idStr := strings.TrimPrefix(r.URL.Path, "/items/")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid id"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			it, ok := st.get(id)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, it)
		case http.MethodPut:
			var input item
			if err := decodeBody(r, &input); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
				return
			}
			if input.Name == "" || input.Status == "" {
				writeJSON(
					w,
					http.StatusBadRequest,
					map[string]any{"error": "name and status required"},
				)
				return
			}
			updated, ok := st.update(id, item{Name: input.Name, Status: input.Status})
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, updated)
		case http.MethodDelete:
			if ok := st.delete(id); !ok {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		}
	})

	server := &http.Server{Addr: ":8080"}
	log.Println("http-crud server listening on :8080")
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

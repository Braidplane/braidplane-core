package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const (
	githubToken = "ghp_mock"
	gitlabToken = "glpat-mock"
)

type proposal struct {
	ID           int64   `json:"id,omitempty"`
	Number       int64   `json:"number,omitempty"`
	IID          int64   `json:"iid,omitempty"`
	HTMLURL      string  `json:"html_url,omitempty"`
	WebURL       string  `json:"web_url,omitempty"`
	State        string  `json:"state"`
	Title        string  `json:"title"`
	Body         string  `json:"body,omitempty"`
	Description  string  `json:"description,omitempty"`
	SourceBranch string  `json:"source_branch,omitempty"`
	TargetBranch string  `json:"target_branch,omitempty"`
	SHA          string  `json:"sha,omitempty"`
	Head         *branch `json:"head,omitempty"`
	Base         *branch `json:"base,omitempty"`
}

type branch struct {
	Ref string `json:"ref"`
	SHA string `json:"sha,omitempty"`
}

type store struct {
	mu           sync.Mutex
	nextGitHubID int64
	nextGitLabID int64
	github       map[int64]proposal
	gitlab       map[int64]proposal
}

func newStore() *store {
	s := &store{}
	s.reset()
	return s
}

func (s *store) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextGitHubID = 1
	s.nextGitLabID = 1
	s.github = make(map[int64]proposal)
	s.gitlab = make(map[int64]proposal)
}

func (s *store) listGitHub(head, base string) []proposal {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := []proposal{}
	for _, item := range s.github {
		if item.State != "open" {
			continue
		}
		if head != "" && "acme:"+item.Head.Ref != head {
			continue
		}
		if base != "" && item.Base.Ref != base {
			continue
		}
		results = append(results, item)
	}
	return results
}

func (s *store) createGitHub(req map[string]string) proposal {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextGitHubID
	s.nextGitHubID++
	item := proposal{
		Number:  id,
		HTMLURL: "https://github.example.test/acme/pipelines/pull/" + strconv.FormatInt(id, 10),
		State:   "open",
		Title:   req["title"],
		Body:    req["body"],
		Head:    &branch{Ref: req["head"], SHA: "github-sha-" + strconv.FormatInt(id, 10)},
		Base:    &branch{Ref: req["base"]},
	}
	s.github[id] = item
	return item
}

func (s *store) updateGitHub(number int64, req map[string]string) (proposal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.github[number]
	if !ok {
		return proposal{}, false
	}
	item.Title = req["title"]
	item.Body = req["body"]
	item.Head.SHA = "github-updated-" + strconv.FormatInt(number, 10)
	s.github[number] = item
	return item, true
}

func (s *store) listGitLab(source, target string) []proposal {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := []proposal{}
	for _, item := range s.gitlab {
		if item.State != "opened" {
			continue
		}
		if source != "" && item.SourceBranch != source {
			continue
		}
		if target != "" && item.TargetBranch != target {
			continue
		}
		results = append(results, item)
	}
	return results
}

func (s *store) createGitLab(req proposal) proposal {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextGitLabID
	s.nextGitLabID++
	item := proposal{
		IID:          id,
		WebURL:       "https://gitlab.example.test/acme/platform/pipelines/-/merge_requests/" + strconv.FormatInt(id, 10),
		State:        "opened",
		Title:        req.Title,
		Description:  req.Description,
		SourceBranch: req.SourceBranch,
		TargetBranch: req.TargetBranch,
		SHA:          "gitlab-sha-" + strconv.FormatInt(id, 10),
	}
	s.gitlab[id] = item
	return item
}

func (s *store) updateGitLab(iid int64, req proposal) (proposal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.gitlab[iid]
	if !ok {
		return proposal{}, false
	}
	item.Title = req.Title
	item.Description = req.Description
	item.SHA = "gitlab-updated-" + strconv.FormatInt(iid, 10)
	s.gitlab[iid] = item
	return item, true
}

func main() {
	state := newStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		state.reset()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/repos/acme/pipelines/pulls", githubPullsHandler(state))
	mux.HandleFunc("/repos/acme/pipelines/pulls/", githubPullHandler(state))
	mux.HandleFunc("/api/v4/projects/", gitlabProjectHandler(state))

	log.Println("git-provider-mock server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func githubPullsHandler(state *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizedGitHub(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "bad token ghp_mock"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, state.listGitHub(r.URL.Query().Get("head"), r.URL.Query().Get("base")))
		case http.MethodPost:
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid json"})
				return
			}
			writeJSON(w, http.StatusCreated, state.createGitHub(req))
		default:
			http.NotFound(w, r)
		}
	}
}

func githubPullHandler(state *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizedGitHub(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "bad token ghp_mock"})
			return
		}
		if r.Method != http.MethodPatch {
			http.NotFound(w, r)
			return
		}
		number, err := parseTrailingID(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid pull number"})
			return
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid json"})
			return
		}
		item, ok := state.updateGitHub(number, req)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "pull request not found"})
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}

func gitlabProjectHandler(state *store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizedGitLab(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "bad token glpat-mock"})
			return
		}
		if !strings.Contains(r.URL.EscapedPath(), "/projects/acme%2Fplatform%2Fpipelines/merge_requests") {
			http.NotFound(w, r)
			return
		}

		if strings.HasSuffix(r.URL.EscapedPath(), "/merge_requests") {
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, http.StatusOK, state.listGitLab(
					r.URL.Query().Get("source_branch"),
					r.URL.Query().Get("target_branch"),
				))
			case http.MethodPost:
				var req proposal
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid json"})
					return
				}
				writeJSON(w, http.StatusCreated, state.createGitLab(req))
			default:
				http.NotFound(w, r)
			}
			return
		}

		if r.Method == http.MethodPut {
			iid, err := parseTrailingID(r.URL.EscapedPath())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid merge request IID"})
				return
			}
			var req proposal
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid json"})
				return
			}
			item, ok := state.updateGitLab(iid, req)
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "merge request not found"})
				return
			}
			writeJSON(w, http.StatusOK, item)
			return
		}

		http.NotFound(w, r)
	}
}

func authorizedGitHub(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+githubToken
}

func authorizedGitLab(r *http.Request) bool {
	return r.Header.Get("PRIVATE-TOKEN") == gitlabToken
}

func parseTrailingID(path string) (int64, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

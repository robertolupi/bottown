package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// REST front end for the forge operations in forge.go, used by the bottown
// CLI that agents run in their worktrees:
//
//	GET  /v1/forge/issue?repo=&number=
//	GET  /v1/forge/issue/attachment?repo=&number=&id=   (the file's bytes)
//	POST /v1/forge/issue    {repo, title, body}
//	GET  /v1/forge/pr?repo=&number=&diff=1
//	POST /v1/forge/pr       {title, body}
//	POST /v1/forge/comment  {repo, number, body}
//	POST /v1/forge/review   {event, body, commit_id}
//
// repo and number default to the run's.

func (s *Server) forgeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/forge/issue", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opIssue(r.Context(), Who(r.Context()), targetFromQuery(r))
		writeOp(w, out, err)
	})))
	mux.Handle("GET /v1/forge/issue/attachment", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if id <= 0 {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		d, err := s.opAttachment(r.Context(), Who(r.Context()), targetFromQuery(r), id)
		if err != nil {
			writeError(w, errStatus(err), err.Error())
			return
		}
		defer d.Body.Close()
		ct := d.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if d.Length >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(d.Length, 10))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, d.Body)
	})))
	mux.Handle("GET /v1/forge/issues", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opIssueList(r.Context(), Who(r.Context()), IssueListIn{
			Repo:  r.URL.Query().Get("repo"),
			State: r.URL.Query().Get("state"),
		})
		writeOp(w, out, err)
	})))
	mux.Handle("POST /v1/forge/issue", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in IssueCreateIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opIssueCreate(r.Context(), Who(r.Context()), in)
		writeOp(w, out, err)
	})))
	mux.Handle("GET /v1/forge/pr", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opPR(r.Context(), Who(r.Context()), targetFromQuery(r), r.URL.Query().Get("diff") != "")
		writeOp(w, out, err)
	})))
	mux.Handle("GET /v1/forge/prs", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opPRList(r.Context(), Who(r.Context()), PRListIn{
			Repo:  r.URL.Query().Get("repo"),
			State: r.URL.Query().Get("state"),
		})
		writeOp(w, out, err)
	})))
	mux.Handle("POST /v1/forge/pr", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in PRCreateIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opPRCreate(r.Context(), Who(r.Context()), in)
		writeOp(w, out, err)
	})))
	mux.Handle("POST /v1/forge/comment", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in CommentIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opComment(r.Context(), Who(r.Context()), in)
		writeOp(w, out, err)
	})))
	mux.Handle("POST /v1/forge/review", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in ReviewIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opReview(r.Context(), Who(r.Context()), in)
		writeOp(w, out, err)
	})))
}

func targetFromQuery(r *http.Request) Target {
	n, _ := strconv.ParseInt(r.URL.Query().Get("number"), 10, 64)
	return Target{Repo: r.URL.Query().Get("repo"), Number: n}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func writeOp(w http.ResponseWriter, out any, err error) {
	if err != nil {
		writeError(w, errStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

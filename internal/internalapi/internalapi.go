// Package internalapi is the API kkids-sync-web serves to the reconciler on
// a separate port. Calls always go from the reconciler to web; web has no
// way to make the reconciler do anything.
//
//	POST /internal/v1/snapshot  record what the reconciler read
//	GET  /internal/v1/plan      what to add to / remove from each kids profile
//	POST /internal/v1/jobs      record a job the reconciler ran
//	GET  /internal/v1/profiles  family members' dashboard profile names and IDs
//	POST /internal/v1/profiles  link profile names to the IDs the reconciler saw
//
// Requests carry "Authorization: Bearer <token>". A NetworkPolicy limits the
// port to the reconciler as well.
package internalapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nasa9084/kkids-sync/internal/store"
)

// Store is what the handler needs from the database.
type Store interface {
	ApplySnapshot(ctx context.Context, snap store.LibrarySnapshot) (store.SnapshotResult, error)
	Plan(ctx context.Context) (map[string]store.Diff, error)
	RecordJob(ctx context.Context, id int64, kind, result, detail string, started time.Time) (int64, error)
	Profiles(ctx context.Context) ([]store.Profile, error)
	LinkProfiles(ctx context.Context, ids map[string]string) (int, error)
}

// JobReport is the body of POST /internal/v1/jobs.
type JobReport struct {
	// ID, when set, updates the job StartJob created instead of adding one.
	ID        int64     `json:"id,omitempty"`
	Kind      string    `json:"kind"`
	Result    string    `json:"result"`
	Detail    string    `json:"detail"`
	StartedAt time.Time `json:"started_at"`
}

// maxSnapshotBytes bounds a snapshot upload (a 2,700-book library is ~0.7 MB).
const maxSnapshotBytes = 32 << 20

// Handler serves the internal API.
func Handler(db Store, token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/snapshot", func(w http.ResponseWriter, r *http.Request) {
		var snap store.LibrarySnapshot
		if err := json.NewDecoder(io.LimitReader(r.Body, maxSnapshotBytes)).Decode(&snap); err != nil {
			http.Error(w, "bad snapshot: "+err.Error(), http.StatusBadRequest)
			return
		}
		res, err := db.ApplySnapshot(r.Context(), snap)
		if errors.Is(err, store.ErrIncompleteSnapshot) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err != nil {
			log.Error("apply snapshot", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		log.Info("snapshot applied", "books", len(snap.Books), "new", res.NewBooks, "removed", res.RemovedBooks, "profiles", res.Profiles, "incremental", snap.Incremental)
		writeJSON(w, res)
	})
	mux.HandleFunc("GET /internal/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		plan, err := db.Plan(r.Context())
		if err != nil {
			log.Error("plan", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, plan)
	})
	mux.HandleFunc("POST /internal/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var j JobReport
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&j); err != nil || j.Kind == "" || j.Result == "" {
			http.Error(w, "bad job report", http.StatusBadRequest)
			return
		}
		id, err := db.RecordJob(r.Context(), j.ID, j.Kind, j.Result, j.Detail, j.StartedAt)
		if err != nil {
			log.Error("add job", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			ID int64 `json:"id"`
		}{id})
	})
	mux.HandleFunc("GET /internal/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
		ps, err := db.Profiles(r.Context())
		if err != nil {
			log.Error("profiles", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if ps == nil {
			ps = []store.Profile{}
		}
		writeJSON(w, ps)
	})
	mux.HandleFunc("POST /internal/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
		var ids map[string]string
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&ids); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n, err := db.LinkProfiles(r.Context(), ids)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Info("profiles linked", "linked", n)
		writeJSON(w, map[string]int{"linked": n})
	})
	return requireToken(token, mux)
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Client calls the internal API from the reconciler.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PostSnapshot sends a snapshot to web.
func (c *Client) PostSnapshot(ctx context.Context, snap any) (store.SnapshotResult, error) {
	var res store.SnapshotResult
	err := c.do(ctx, http.MethodPost, "/internal/v1/snapshot", snap, &res)
	return res, err
}

// Plan fetches what to change per kids profile.
func (c *Client) Plan(ctx context.Context) (map[string]store.Diff, error) {
	var plan map[string]store.Diff
	err := c.do(ctx, http.MethodGet, "/internal/v1/plan", nil, &plan)
	return plan, err
}

// ReportJob records a job in web's history.
func (c *Client) ReportJob(ctx context.Context, j JobReport) error {
	return c.do(ctx, http.MethodPost, "/internal/v1/jobs", j, nil)
}

// StartJob records a job as running and returns its id, for the final
// ReportJob to update.
func (c *Client) StartJob(ctx context.Context, kind, detail string, started time.Time) (int64, error) {
	var res struct {
		ID int64 `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/internal/v1/jobs", JobReport{Kind: kind, Result: store.JobRunning, Detail: detail, StartedAt: started}, &res)
	return res.ID, err
}

// Profiles fetches the family members' profile names and IDs.
func (c *Client) Profiles(ctx context.Context) ([]store.Profile, error) {
	var ps []store.Profile
	err := c.do(ctx, http.MethodGet, "/internal/v1/profiles", nil, &ps)
	return ps, err
}

// LinkProfiles sends name → ID pairs seen on the dashboard.
func (c *Client) LinkProfiles(ctx context.Context, ids map[string]string) (int, error) {
	var res struct {
		Linked int `json:"linked"`
	}
	err := c.do(ctx, http.MethodPost, "/internal/v1/profiles", ids, &res)
	return res.Linked, err
}

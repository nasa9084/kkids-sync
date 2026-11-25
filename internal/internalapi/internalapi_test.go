package internalapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/nasa9084/kkids-sync/internal/store"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.AddMember(ctx, store.Member{Name: "Mom", Email: "mom@example.com", Role: store.RoleFamily, KidsProfileID: "P1"}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(Handler(db, "s3cret", log))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "s3cret"}
	snap := store.LibrarySnapshot{
		Books:    []store.Book{{ASIN: "A", Title: "a"}, {ASIN: "B", Title: "b"}},
		Shared:   map[string][]string{"P1": {"A"}},
		Complete: true,
	}
	res, err := c.PostSnapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewBooks != 2 || res.Profiles != 1 {
		t.Errorf("PostSnapshot = %+v", res)
	}
	plan, err := c.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Not bootstrapped yet, so nothing is removed.
	if want := (map[string]store.Diff{"P1": {}}); !reflect.DeepEqual(plan, want) {
		t.Errorf("Plan = %+v, want %+v", plan, want)
	}

	id, err := c.StartJob(ctx, "apply", "1件を反映中", time.Now())
	if err != nil || id == 0 {
		t.Fatalf("StartJob = %d, %v", id, err)
	}
	if jobs, _ := db.Jobs(ctx, 10); jobs[0].ID != id || jobs[0].Result != store.JobRunning || !jobs[0].FinishedAt.IsZero() {
		t.Errorf("running job = %+v", jobs[0])
	}
	if err := c.ReportJob(ctx, JobReport{ID: id, Kind: "apply", Result: "ok", Detail: "追加 1"}); err != nil {
		t.Errorf("ReportJob: %v", err)
	}
	if jobs, _ := db.Jobs(ctx, 10); len(jobs) != 2 || jobs[0].ID != id || jobs[0].Result != "ok" || jobs[0].FinishedAt.IsZero() {
		t.Errorf("finished job = %+v (the running row is updated, not added)", jobs)
	}
	if err := c.ReportJob(ctx, JobReport{}); err == nil {
		t.Error("empty job report accepted")
	}

	if _, err := db.AddMember(ctx, store.Member{Name: "Sis", Email: "sis@example.com", Role: store.RoleFamily, KidsProfileName: "あやね"}); err != nil {
		t.Fatal(err)
	}
	ps, err := c.Profiles(ctx)
	if err != nil || len(ps) != 1 || ps[0].Name != "あやね" || ps[0].ID != "" {
		t.Fatalf("Profiles = %+v, %v", ps, err)
	}
	if n, err := c.LinkProfiles(ctx, map[string]string{"あやね": "P2"}); err != nil || n != 1 {
		t.Errorf("LinkProfiles = %d, %v", n, err)
	}

	snap.Complete = false
	if _, err := c.PostSnapshot(ctx, snap); err == nil {
		t.Error("incomplete snapshot accepted")
	}

	bad := &Client{BaseURL: srv.URL, Token: "wrong"}
	if _, err := bad.Plan(ctx); err == nil {
		t.Error("wrong token accepted")
	}
}

func TestEmptyTokenRefusesEverything(t *testing.T) {
	h := Handler(nil, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest("GET", "/internal/v1/plan", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rec.Code)
	}
}

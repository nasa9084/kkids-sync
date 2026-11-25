package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nasa9084/kkids-sync/internal/amazon"
	"github.com/nasa9084/kkids-sync/internal/internalapi"
	"github.com/nasa9084/kkids-sync/internal/store"
	"github.com/nasa9084/kkids-sync/internal/titles"
)

// apply fetches the plan from web and carries it out on the dashboard, one
// toggle at a time. Every change is reported as one job.
// applyResult tells the caller whether apply touched the dashboard.
type applyResult struct{ changed int }

func apply(ctx context.Context, log *slog.Logger, client *internalapi.Client, state string, maxChanges int, res *applyResult) (err error) {
	if client == nil {
		return errors.New("apply needs -web")
	}
	started := time.Now()
	var jobID int64 // set once the run is shown as running
	report := func(result, detail string) {
		if err := client.ReportJob(context.WithoutCancel(ctx), internalapi.JobReport{ID: jobID, Kind: "apply", Result: result, Detail: detail, StartedAt: started}); err != nil {
			log.Warn("report job", "err", err)
		}
	}

	profs, err := client.Profiles(ctx)
	if err != nil {
		return err
	}
	var s *amazon.Session
	defer func() {
		if s != nil {
			if rerr := s.ResetFilter(context.WithoutCancel(ctx)); rerr != nil {
				log.Warn("reset filter", "err", rerr)
			}
			err = errors.Join(err, s.Close())
		}
	}()
	open := func() error {
		if s != nil {
			return nil
		}
		var err error
		s, err = amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, AllowExpectedWrites: true, Logger: log, Headless: headless})
		if err != nil {
			return err
		}
		return s.OpenAddContent(ctx)
	}
	if unlinked(profs) {
		if err := open(); err != nil {
			report("failed", err.Error())
			return err
		}
		if profs, err = link(ctx, log, client, s); err != nil {
			report("failed", err.Error())
			return err
		}
	}
	names := map[string]string{}
	for _, p := range profs {
		if p.ID != "" {
			names[p.ID] = p.Name
		}
	}

	plan, err := client.Plan(ctx)
	if err != nil {
		return err
	}
	bo := loadBackoff(backoffPath(state))
	defer func() {
		if err := bo.save(); err != nil {
			log.Warn("save failures", "err", err)
		}
	}()
	plan, held := bo.filter(plan, time.Now())
	if held > 0 {
		log.Info("changes held after earlier failures", "held", held)
	}
	plan, slow := bo.limitUnsearchable(plan, listFallbacks)
	plan, total, deferred := limitPlan(plan, maxChanges)
	deferred += slow
	if total == 0 {
		log.Info("nothing to apply", "held", held)
		return nil
	}
	if deferred > 0 {
		log.Info("changes spread over runs", "now", total, "later", deferred)
	}
	// A run can take a quarter of an hour: show it on the job page now.
	if id, err := client.StartJob(ctx, "apply", fmt.Sprintf("%d件を反映中(残り%d件は次回以降)", total, deferred), started); err != nil {
		log.Warn("report job start", "err", err)
	} else {
		jobID = id
	}
	if err := open(); err != nil {
		report("failed", err.Error())
		return err
	}

	var done, skipped int
	listBudget := listFallbacks
	var failures []string
	profiles := make([]string, 0, len(plan))
	for p := range plan {
		profiles = append(profiles, p)
	}
	sort.Strings(profiles)
	for _, profile := range profiles {
		d := plan[profile]
		if len(d.Add)+len(d.Remove) == 0 {
			continue
		}
		if err := s.SelectChild(ctx, profile, names[profile]); err != nil {
			if errors.Is(err, amazon.ErrSignedOut) || ctx.Err() != nil {
				report("failed", err.Error())
				return err
			}
			failures = append(failures, fmt.Sprintf("%s: %v", shortID(profile), err))
			continue
		}
		for i, step := range steps(d) {
			// The page grows with every search (it ran out of memory at
			// 1.5GiB once without the list): start afresh every ten books.
			if i > 0 && i%10 == 0 {
				if err := s.OpenAddContent(ctx); err != nil {
					report("failed", err.Error())
					return err
				}
				if err := s.SelectChild(ctx, profile, names[profile]); err != nil {
					failures = append(failures, fmt.Sprintf("%s: %v", shortID(profile), err))
					break
				}
			}
			changed, err := toggle(ctx, log, s, profile, names[profile], step, d.Titles[step.asin], &listBudget, bo)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					report("failed", err.Error())
					return err
				}
				log.Warn("toggle failed", "profile", shortID(profile), "asin", step.asin, "err", err)
				failures = append(failures, fmt.Sprintf("%s %s: %v", shortID(profile), step.asin, err))
				bo.failed(profile, step.asin, time.Now())
			case changed:
				done++
				bo.succeeded(profile, step.asin)
				log.Info("toggled", "profile", shortID(profile), "asin", step.asin, "add", step.add)
			default:
				skipped++
				bo.succeeded(profile, step.asin)
			}
			if err := amazon.Sleep(ctx, 3*time.Second, 9*time.Second); err != nil {
				report("failed", err.Error())
				return err
			}
		}
	}

	if res != nil {
		// Skipped means the share state web has is stale: sync either way.
		res.changed = done + skipped
	}
	detail := fmt.Sprintf("変更 %d件・すでに反映済み %d件・失敗 %d件", done, skipped, len(failures))
	if deferred > 0 {
		detail += fmt.Sprintf("・残り%d件は次回以降", deferred)
	}
	if held > 0 {
		detail += fmt.Sprintf("・前に失敗した%d件は時間をおいて再試行", held)
	}
	if len(failures) > 0 {
		// Books that fail are retried later (backoff) and listed on the job
		// page; the run itself did its work, so it does not fail the Job.
		detail += "\n" + strings.Join(failures, "\n")
		report("partial", detail)
		log.Warn("some changes failed", "failed", len(failures), "of", total)
		return nil
	}
	report("ok", detail)
	log.Info("applied", "changed", done, "skipped", skipped)
	return nil
}

// limitPlan keeps at most max changes for this run, removals first, taking
// turns between profiles so that one member's big change (a whole series,
// an emptied shelf) does not hold up the others. The rest is left for the
// next runs. It returns the trimmed plan, its size, and how many changes
// were left.
func limitPlan(plan map[string]store.Diff, max int) (out map[string]store.Diff, total, deferred int) {
	profiles := make([]string, 0, len(plan))
	all := 0
	out = map[string]store.Diff{}
	for p, d := range plan {
		profiles = append(profiles, p)
		all += len(d.Add) + len(d.Remove)
		out[p] = store.Diff{Titles: d.Titles}
	}
	sort.Strings(profiles)
	take := func(get func(store.Diff) []string, put func(*store.Diff, string)) {
		for i := 0; total < max; i++ {
			any := false
			for _, p := range profiles {
				list := get(plan[p])
				if i >= len(list) {
					continue
				}
				any = true
				if total < max {
					d := out[p]
					put(&d, list[i])
					out[p] = d
					total++
				}
			}
			if !any {
				return
			}
		}
	}
	take(func(d store.Diff) []string { return d.Remove }, func(d *store.Diff, a string) { d.Remove = append(d.Remove, a) })
	take(func(d store.Diff) []string { return d.Add }, func(d *store.Diff, a string) { d.Add = append(d.Add, a) })
	return out, total, all - total
}

func unlinked(ps []store.Profile) bool {
	for _, p := range ps {
		if p.Name != "" && p.ID == "" {
			return true
		}
	}
	return false
}

// link reads the profile names and IDs from the dashboard's filter, sends
// them to web and returns the members' profiles afterwards.
func link(ctx context.Context, log *slog.Logger, client *internalapi.Client, s *amazon.Session) ([]store.Profile, error) {
	ids, err := s.ListChildren(ctx)
	if err != nil {
		return nil, err
	}
	for name, id := range ids {
		log.Info("profile on dashboard", "name", name, "id", shortID(id))
	}
	n, err := client.LinkProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	log.Info("profiles linked", "updated", n)
	profs, err := client.Profiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range profs {
		if p.ID == "" {
			log.Warn("no profile with this name on the dashboard", "name", p.Name)
		}
	}
	return profs, nil
}

// linkProfiles is the link-profiles command.
func linkProfiles(ctx context.Context, log *slog.Logger, client *internalapi.Client, state string) (err error) {
	if client == nil {
		return errors.New("link-profiles needs -web")
	}
	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, Logger: log, Headless: headless})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	if err := s.OpenAddContent(ctx); err != nil {
		return err
	}
	_, err = link(ctx, log, client, s)
	return err
}

type step struct {
	asin string
	add  bool
}

// steps orders removals first, then additions.
func steps(d store.Diff) []step {
	var out []step
	for _, a := range d.Remove {
		out = append(out, step{a, false})
	}
	for _, a := range d.Add {
		out = append(out, step{a, true})
	}
	return out
}

// query is what to type into the dashboard's search box for a title: the
// title without its label, as a person would type it.
// queries lists what to type into the dashboard's search for a title, in
// order: the title without its label, the series name, the full title.
// The dashboard's search does not find every title by every spelling (for
// example "横浜駅SF(3)"), so the later ones are fallbacks.
func queries(title string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		if r := []rune(q); len(r) > 40 {
			q = string(r[:40])
		}
		if q != "" && !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	display := titles.DisplayTitle(title)
	add(display)
	if name, _, ok := titles.ParseSeries(title); ok {
		add(name)
	}
	add(title)
	// The search finds nothing for some symbols ("O/A(3)"): try words only.
	add(strings.Join(strings.FieldsFunc(display, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}), " "))
	return out
}

// listFallbacks is how many books per run may be looked for by scrolling
// the list when no search shows them; each can take a few minutes.
const listFallbacks = 5

// toggle tries each query until the search shows the book, then (while
// *listBudget lasts) scrolls the list for it.
func toggle(ctx context.Context, log *slog.Logger, s *amazon.Session, profile, name string, st step, title string, listBudget *int, bo *backoff) (bool, error) {
	var err error
	qs := queries(title)
	if bo.Unsearchable[st.asin] {
		qs = nil // the search never shows it: go to the list
		err = fmt.Errorf("%s: %w (before)", st.asin, amazon.ErrNotInSearch)
	}
	for i, q := range qs {
		if i > 0 {
			log.Info("retrying search", "asin", st.asin, "query", q)
			if err := amazon.Sleep(ctx, 2*time.Second, 5*time.Second); err != nil {
				return false, err
			}
		}
		var changed bool
		changed, err = s.Toggle(ctx, profile, st.asin, q, st.add)
		if !errors.Is(err, amazon.ErrNotInSearch) {
			return changed, err
		}
	}
	bo.Unsearchable[st.asin] = true
	if *listBudget <= 0 {
		return false, err
	}
	*listBudget--
	log.Info("looking for the book in the list", "asin", st.asin, "search", err)
	if err := amazon.Sleep(ctx, 2*time.Second, 5*time.Second); err != nil {
		return false, err
	}
	changed, err := s.ToggleInList(ctx, profile, st.asin, st.add)
	// The scrolled list holds hundreds of books in the page (it ran Chromium
	// out of memory once): load the page afresh and select the child again.
	if rerr := s.OpenAddContent(ctx); rerr != nil {
		return changed, errors.Join(err, rerr)
	}
	if rerr := s.SelectChild(ctx, profile, name); rerr != nil {
		return changed, errors.Join(err, rerr)
	}
	return changed, err
}

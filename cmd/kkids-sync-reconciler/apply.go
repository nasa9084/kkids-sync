package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nasa9084/kkids-sync/internal/amazon"
	"github.com/nasa9084/kkids-sync/internal/internalapi"
	"github.com/nasa9084/kkids-sync/internal/store"
	"github.com/nasa9084/kkids-sync/internal/titles"
)

// errTooManyChanges stops a plan that is larger than expected; a person
// must look at it first (exit code 4).
var errTooManyChanges = errors.New("too many changes")

// apply fetches the plan from web and carries it out on the dashboard, one
// toggle at a time. Every change is reported as one job.
func apply(ctx context.Context, log *slog.Logger, client *internalapi.Client, state string, maxChanges int) (err error) {
	if client == nil {
		return errors.New("apply needs -web")
	}
	started := time.Now()
	plan, err := client.Plan(ctx)
	if err != nil {
		return err
	}
	total := 0
	for _, d := range plan {
		total += len(d.Add) + len(d.Remove)
	}
	if total == 0 {
		log.Info("nothing to apply")
		return nil
	}
	report := func(result, detail string) {
		if err := client.ReportJob(context.WithoutCancel(ctx), internalapi.JobReport{Kind: "apply", Result: result, Detail: detail, StartedAt: started}); err != nil {
			log.Warn("report job", "err", err)
		}
	}
	if total > maxChanges {
		report("stopped", fmt.Sprintf("変更が%d件あり、上限%d件を超えたため実行しませんでした", total, maxChanges))
		return fmt.Errorf("%w: %d planned, limit %d", errTooManyChanges, total, maxChanges)
	}

	s, err := amazon.Open(amazon.Options{StatePath: state, ReadOnly: true, AllowExpectedWrites: true, Logger: log})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	if err := s.OpenAddContent(ctx); err != nil {
		report("failed", err.Error())
		return err
	}

	var done, skipped int
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
		if err := s.SelectChild(ctx, profile); err != nil {
			if errors.Is(err, amazon.ErrSignedOut) || ctx.Err() != nil {
				report("failed", err.Error())
				return err
			}
			failures = append(failures, fmt.Sprintf("%s: %v", shortID(profile), err))
			continue
		}
		for _, step := range steps(d) {
			changed, err := s.Toggle(ctx, profile, step.asin, query(d.Titles[step.asin]), step.add)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					report("failed", err.Error())
					return err
				}
				log.Warn("toggle failed", "profile", shortID(profile), "asin", step.asin, "err", err)
				failures = append(failures, fmt.Sprintf("%s %s: %v", shortID(profile), step.asin, err))
			case changed:
				done++
				log.Info("toggled", "profile", shortID(profile), "asin", step.asin, "add", step.add)
			default:
				skipped++
			}
			if err := amazon.Sleep(ctx, 3*time.Second, 9*time.Second); err != nil {
				report("failed", err.Error())
				return err
			}
		}
	}

	detail := fmt.Sprintf("変更 %d件・すでに反映済み %d件・失敗 %d件", done, skipped, len(failures))
	if len(failures) > 0 {
		detail += "\n" + strings.Join(failures, "\n")
		report("partial", detail)
		return fmt.Errorf("%d of %d changes failed", len(failures), total)
	}
	report("ok", detail)
	log.Info("applied", "changed", done, "skipped", skipped)
	return nil
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
func query(title string) string {
	q := titles.DisplayTitle(title)
	if r := []rune(q); len(r) > 40 {
		q = string(r[:40])
	}
	return q
}

// Command kkids-sync-web serves ほんだな, the page family members use to pick
// the books shared with their Amazon Kids profiles, and owns the database.
//
//	kkids-sync-web serve                        run the web server
//	kkids-sync-web member add -name … -email …  register someone who may sign in
//	kkids-sync-web member list
//	kkids-sync-web bootstrap                    copy today's share state into selections (first run only)
//
// Configuration comes from the environment:
//
//	KKIDS_DB                  SQLite file (default kkids-sync.db)
//	KKIDS_LISTEN              address for family and admin pages (default :8080)
//	KKIDS_INTERNAL_LISTEN     address for the reconciler's internal API (default :8081)
//	KKIDS_INTERNAL_TOKEN      bearer token the reconciler must send; the internal API is off without it
//	KKIDS_ACCESS_TEAM_DOMAIN  Cloudflare Access team domain, e.g. example.cloudflareaccess.com
//	KKIDS_ACCESS_AUD          Access application audience tag
//	KKIDS_DEV_EMAIL           skip Access and act as this address (local development only;
//	                          the server then listens on 127.0.0.1 only)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/nasa9084/kkids-sync/internal/auth"
	"github.com/nasa9084/kkids-sync/internal/internalapi"
	"github.com/nasa9084/kkids-sync/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log, os.Args[1:], os.Stdout); err != nil {
		log.Error("failed", "err", err)
		os.Exit(1)
	}
}

func usage() error {
	return errors.New("usage: kkids-sync-web serve | member add -name NAME -email EMAIL [-role family|admin] [-kids-profile ID] | member list | bootstrap")
}

func run(log *slog.Logger, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, envOr("KKIDS_DB", "kkids-sync.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	switch args[0] {
	case "serve":
		return serve(ctx, log, db)
	case "bootstrap":
		n, err := db.BootstrapFromShared(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "selected %d shared books for their members and made them visible\n", n)
		return nil
	case "member":
		if len(args) < 2 {
			return usage()
		}
		switch args[1] {
		case "add":
			return memberAdd(ctx, db, args[2:], out)
		case "list":
			return memberList(ctx, db, out)
		}
	}
	return usage()
}

func serve(ctx context.Context, log *slog.Logger, db *store.Store) error {
	listen := envOr("KKIDS_LISTEN", ":8080")
	var verifier auth.EmailVerifier
	if dev := os.Getenv("KKIDS_DEV_EMAIL"); dev != "" {
		host, port, err := net.SplitHostPort(listen)
		if err != nil {
			return err
		}
		if host != "127.0.0.1" && host != "localhost" {
			listen = net.JoinHostPort("127.0.0.1", port)
		}
		log.Warn("development mode: Cloudflare Access is not checked", "as", dev, "listen", listen)
		verifier = auth.FixedEmail(dev)
	} else {
		v, err := auth.NewAccessVerifier(ctx, os.Getenv("KKIDS_ACCESS_TEAM_DOMAIN"), os.Getenv("KKIDS_ACCESS_AUD"))
		if err != nil {
			return fmt.Errorf("%w (set KKIDS_ACCESS_TEAM_DOMAIN and KKIDS_ACCESS_AUD, or KKIDS_DEV_EMAIL for local use)", err)
		}
		verifier = v
	}

	mux := http.NewServeMux()
	// Probes come from the kubelet, not through Access.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok\n")
	})

	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		m, _ := auth.MemberFrom(r.Context())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "ほんだな: こんにちは、%sさん\n", m.Name)
	})
	app.Handle("GET /admin/{$}", auth.RequireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ほんだな 管理\n")
	})))
	mux.Handle("/", auth.Middleware(verifier, db, log)(app))

	servers := []*http.Server{{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}}
	if token := os.Getenv("KKIDS_INTERNAL_TOKEN"); token != "" {
		servers = append(servers, &http.Server{
			Addr:              envOr("KKIDS_INTERNAL_LISTEN", ":8081"),
			Handler:           internalapi.Handler(db, token, log),
			ReadHeaderTimeout: 10 * time.Second,
		})
	} else {
		log.Warn("KKIDS_INTERNAL_TOKEN is not set: the reconciler's internal API is off")
	}

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { errc <- srv.ListenAndServe() }()
		log.Info("listening", "addr", srv.Addr)
	}
	var err error
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		err = errors.Join(err, srv.Shutdown(shutdown))
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func memberAdd(ctx context.Context, db *store.Store, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("member add", flag.ContinueOnError)
	name := fs.String("name", "", "display name")
	email := fs.String("email", "", "address used to sign in through Cloudflare Access")
	role := fs.String("role", string(store.RoleFamily), "family or admin")
	profile := fs.String("kids-profile", "", "Amazon Kids profile ID (amzn1.account.…), family only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *email == "" {
		return errors.New("-name and -email are required")
	}
	if store.Role(*role) == store.RoleFamily && *profile == "" {
		return errors.New("-kids-profile is required for family members")
	}
	m, err := db.AddMember(ctx, store.Member{Name: *name, Email: *email, Role: store.Role(*role), KidsProfileID: *profile})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "added %s (%s) as %s, id %d\n", m.Name, m.Email, m.Role, m.ID)
	return nil
}

func memberList(ctx context.Context, db *store.Store, out io.Writer) error {
	ms, err := db.Members(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tEMAIL\tROLE\tKIDS PROFILE")
	for _, m := range ms {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", m.ID, m.Name, m.Email, m.Role, m.KidsProfileID)
	}
	return tw.Flush()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

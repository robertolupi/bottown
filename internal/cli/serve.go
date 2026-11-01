package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/spf13/cobra"
)

// containerEnv is the configuration a container process reads from its
// environment (set by the image and by `bottown up`).
type containerEnv struct {
	Instance   string
	Data       string
	Listen     string
	ForgejoURL string
	PublicURL  string // Forgejo as the owner's browser sees it
	Owner      provision.Owner
}

func readContainerEnv() (containerEnv, error) {
	e := containerEnv{
		Instance:   getenv("BOTTOWN_INSTANCE", config.DefaultInstance),
		Data:       getenv("BOTTOWN_DATA", "/data/bottown"),
		Listen:     getenv("BOTTOWN_LISTEN", ":8080"),
		ForgejoURL: getenv("BOTTOWN_FORGEJO_URL", "http://127.0.0.1:3000"),
		PublicURL:  strings.TrimRight(os.Getenv("FORGEJO__server__ROOT_URL"), "/"),
		Owner: provision.Owner{
			Login:    os.Getenv("BOTTOWN_OWNER_LOGIN"),
			Email:    os.Getenv("BOTTOWN_OWNER_EMAIL"),
			FullName: os.Getenv("BOTTOWN_OWNER_FULLNAME"),
		},
	}
	if e.Owner.Login == "" {
		return e, errors.New("BOTTOWN_OWNER_LOGIN is not set (this command runs inside a bottown container)")
	}
	return e, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newReconciler(e containerEnv, log *slog.Logger) (*provision.Reconciler, *secrets.Store, error) {
	store, err := secrets.Open(e.Data)
	if err != nil {
		return nil, nil, err
	}
	desired, err := config.LoadDesired(config.DesiredPath)
	if err != nil {
		return nil, nil, err
	}
	return &provision.Reconciler{
		BaseURL:     e.ForgejoURL,
		Admin:       forgejo.AdminClient{},
		Store:       store,
		Desired:     desired,
		DesiredPath: config.DesiredPath,
		Owner:       e.Owner,
		Log:         log,
	}, store, nil
}

func newServe() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Run the bottown server (inside the container)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
}

func serve(ctx context.Context) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("svc", "bottown")
	e, err := readContainerEnv()
	if err != nil {
		return err
	}
	rec, store, err := newReconciler(e, log)
	if err != nil {
		return err
	}
	fu, err := url.Parse(e.ForgejoURL)
	if err != nil {
		return err
	}
	audit, err := server.OpenAudit(e.Data)
	if err != nil {
		return err
	}
	srv := &server.Server{
		Instance: e.Instance, Version: Version, ForgejoURL: fu, Owner: e.Owner.Login,
		Store: store, Rec: rec, Policy: policy.Default(e.Owner.Login), Log: log,
		Audit:            audit,
		PublicForgejoURL: e.PublicURL,
		ForgejoCookie:    config.ForgejoCookie(e.Instance),
		UICookie:         config.UICookie(e.Instance),
	}
	hs := &http.Server{Addr: e.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("listening", "addr", e.Listen, "instance", e.Instance, "version", Version)

	go reconcileLoop(ctx, rec, srv, log)

	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

// reconcileLoop waits for Forgejo and retries the reconcile with backoff
// until it converges once; the server answers 503 until then.
func reconcileLoop(ctx context.Context, rec *provision.Reconciler, srv *server.Server, log *slog.Logger) {
	if err := rec.WaitReady(ctx); err != nil {
		return
	}
	srv.SetHealth(func(h *server.Health) { h.Forgejo = "ready" })
	backoff := time.Second
	for {
		rep, err := rec.Run(ctx)
		if err == nil {
			// The version endpoint needs a signed-in caller.
			v, _, _ := rec.API(ctx).ServerVersion()
			log.Info("reconciled", "forgejo_version", v, "changes", len(rep.Changes))
			for _, c := range rep.Changes {
				log.Info("change", "what", c)
			}
			srv.SetHealth(func(h *server.Health) {
				h.Reconciled, h.Error, h.Changes, h.ForgejoVersion = true, "", rep.Changes, v
			})
			return
		}
		log.Error("reconcile failed", "err", err, "retry_in", backoff.String())
		srv.SetHealth(func(h *server.Health) { h.Error = fmt.Sprint(err) })
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

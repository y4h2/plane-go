package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/analytic"
	"planego/internal/analyticview"
	"planego/internal/apitoken"
	"planego/internal/archive"
	"planego/internal/asset"
	"planego/internal/auth"
	"planego/internal/config"
	"planego/internal/entityprops"
	"planego/internal/estimate"
	"planego/internal/cycle"
	"planego/internal/db/gen"
	"planego/internal/exporter"
	"planego/internal/external"
	"planego/internal/httpx"
	"planego/internal/instance"
	"planego/internal/intake"
	"planego/internal/issue"
	"planego/internal/issueextra"
	"planego/internal/issuetail"
	"planego/internal/label"
	"planego/internal/module"
	"planego/internal/page"
	"planego/internal/project"
	"planego/internal/search"
	"planego/internal/state"
	"planego/internal/user"
	"planego/internal/userextra"
	"planego/internal/userprops"
	"planego/internal/view"
	"planego/internal/viewextra"
	"planego/internal/webhook"
	"planego/internal/workspace"
	"planego/internal/wslist"
	"planego/internal/wsextra"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	q := gen.New(pool)
	a := auth.New(q, cfg)
	usr := user.New(q)
	inst := instance.New()
	ws := workspace.New(q)
	proj := project.New(q)
	iss := issue.New(q)
	st := state.New(q)
	lb := label.New(q)
	cy := cycle.New(q)
	mo := module.New(q, pool)
	vw := view.New(q)
	upr := userprops.New(q)
	est := estimate.New(q)
	wx := wsextra.New(q)
	as := asset.New(q, cfg)
	sr := search.New(q)
	at := apitoken.New(pool)
	wh := webhook.New(pool)
	ik := intake.New(pool)
	pg := page.New(pool)
	ex := exporter.New(pool)
	ext := external.New()
	an := analytic.New(pool)
	arc := archive.New(pool)
	ve := viewextra.New(pool)
	ixe := issueextra.New(pool)
	ux := userextra.New(pool)
	it := issuetail.New(pool)
	ep := entityprops.New(pool)
	av := analyticview.New(pool)
	wl := wslist.New(pool)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/auth", func(r chi.Router) {
		a.Routes(r)
		// change-password lives under /auth (like sign-in), but requires an
		// authenticated session + CSRF, unlike the rest of the /auth group.
		r.With(a.Require).Post("/change-password/", ux.ChangePassword)
	})

	r.Route("/api", func(r chi.Router) {
		// public: the frontend reads instance config before login; asset
		// upload/serve targets carry no credentials.
		inst.Routes(r)
		as.RoutesPublic(r)
		// everything else requires an authenticated session
		r.Group(func(r chi.Router) {
			r.Use(a.Require)
			usr.Routes(r)
			as.Routes(r)
			ws.Routes(r)
			proj.Routes(r)
			iss.Routes(r)
			iss.RoutesDetail(r)
			iss.RoutesActions(r)
			st.Routes(r)
			lb.Routes(r)
			cy.Routes(r)
			mo.Routes(r)
			vw.Routes(r)
			upr.Routes(r)
			est.Routes(r)
			wx.Routes(r)
			sr.Routes(r)
			at.Routes(r)
			wh.Routes(r)
			ik.Routes(r)
			pg.Routes(r)
			ex.Routes(r)
			ext.Routes(r)
			an.Routes(r)
			arc.Routes(r)
			ve.Routes(r)
			ixe.Routes(r)
			ux.Routes(r)
			it.Routes(r)
			ep.Routes(r)
			av.Routes(r)
			wl.Routes(r)
		})
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("plane-go listening on %s", cfg.Addr)
	log.Fatal(srv.ListenAndServe())
}

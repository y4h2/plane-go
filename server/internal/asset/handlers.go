// Package asset implements a self-contained file-asset store mirroring Plane's
// v2 asset flow: POST to request an upload slot -> the browser POSTs the file to
// the returned URL -> PATCH confirms. Files are stored on local disk and served
// back publicly. This replaces the S3/MinIO presigned-upload flow with something
// that works through the existing proxy without object-storage plumbing.
package asset

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"planego/internal/auth"
	"planego/internal/config"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

type Handler struct {
	q   *gen.Queries
	cfg config.Config
}

func New(q *gen.Queries, cfg config.Config) *Handler {
	_ = os.MkdirAll(cfg.AssetDir, 0o755)
	return &Handler{q: q, cfg: cfg}
}

// Routes registers the authenticated create/confirm endpoints.
func (h *Handler) Routes(r chi.Router) {
	r.Post("/assets/v2/workspaces/{slug}/", h.create)
	r.Patch("/assets/v2/workspaces/{slug}/{asset_id}/", h.markUploaded)
	r.Delete("/assets/v2/workspaces/{slug}/{asset_id}/", h.del)
	r.Post("/assets/v2/workspaces/{slug}/projects/{project_id}/", h.create)
	r.Patch("/assets/v2/workspaces/{slug}/projects/{project_id}/{asset_id}/", h.markUploaded)
	r.Post("/assets/v2/workspaces/{slug}/{entity_id}/bulk/", h.bulk)
	r.Post("/assets/v2/workspaces/{slug}/projects/{project_id}/{entity_id}/bulk/", h.bulk)
	r.Post("/assets/v2/user-assets/", h.create)
	r.Patch("/assets/v2/user-assets/{asset_id}/", h.markUploaded)
	r.Delete("/assets/v2/user-assets/{asset_id}/", h.del)
}

// RoutesPublic registers the endpoints the browser hits without credentials:
// the raw file upload target and the static file server.
func (h *Handler) RoutesPublic(r chi.Router) {
	r.Post("/assets/v2/upload/{asset_id}/", h.receive)
	r.Get("/assets/v2/static/{asset_id}/", h.serve)
}

// workspaceEntityTypes mirrors FileAsset.EntityTypeContext.values (workspace/
// project-scoped uploads); userEntityTypes is the tighter set the user-asset
// endpoint accepts. Unknown values are rejected 400, matching Python.
var workspaceEntityTypes = map[string]bool{
	"ISSUE_ATTACHMENT": true, "ISSUE_DESCRIPTION": true, "COMMENT_DESCRIPTION": true,
	"PAGE_DESCRIPTION": true, "USER_COVER": true, "USER_AVATAR": true,
	"WORKSPACE_LOGO": true, "PROJECT_COVER": true, "DRAFT_ISSUE_ATTACHMENT": true,
	"DRAFT_ISSUE_DESCRIPTION": true,
}
var userEntityTypes = map[string]bool{"USER_AVATAR": true, "USER_COVER": true}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Name             string `json:"name"`
		Type             string `json:"type"`
		Size             int64  `json:"size"`
		EntityType       string `json:"entity_type"`
		EntityIdentifier string `json:"entity_identifier"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// user-assets route has no slug; it accepts only avatar/cover.
	allowed := workspaceEntityTypes
	if chi.URLParam(r, "slug") == "" {
		allowed = userEntityTypes
	}
	if !allowed[body.EntityType] {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid entity type.", "status": false})
		return
	}
	ct := body.Type
	if ct == "" {
		ct = "application/octet-stream"
	}

	var wsID, projID pgtype.UUID
	if slug := chi.URLParam(r, "slug"); slug != "" {
		if ws, err := h.q.GetWorkspaceBySlug(ctx, slug); err == nil {
			wsID = dbx.PgUUID(ws.ID)
		}
	}
	if pid, err := uuid.Parse(chi.URLParam(r, "project_id")); err == nil {
		projID = dbx.PgUUID(pid)
	}

	a, err := h.q.CreateAsset(ctx, gen.CreateAssetParams{
		WorkspaceID: wsID, ProjectID: projID, UserID: dbx.PgUUID(u.ID),
		Name: body.Name, ContentType: ct, Size: body.Size,
		EntityType: body.EntityType, EntityIdentifier: body.EntityIdentifier,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"asset_id":  a.ID.String(),
		"asset_url": "/api/assets/v2/static/" + a.ID.String() + "/",
		"upload_data": map[string]any{
			"url":    h.cfg.PublicURL + "/api/assets/v2/upload/" + a.ID.String() + "/",
			"fields": map[string]any{},
		},
	})
}

// receive stores the uploaded file bytes (public: the uploader sends no cookie).
func (h *Handler) receive(w http.ResponseWriter, r *http.Request) {
	aid, err := uuid.Parse(chi.URLParam(r, "asset_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid upload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "missing file")
		return
	}
	defer file.Close()
	dst, err := os.Create(filepath.Join(h.cfg.AssetDir, aid.String()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "storage error")
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "storage error")
		return
	}
	_ = h.q.MarkAssetUploaded(r.Context(), aid)
	w.WriteHeader(http.StatusNoContent)
}

// bulk confirms a set of already-uploaded assets are associated with an entity.
func (h *Handler) bulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AssetIds []string `json:"asset_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, id := range body.AssetIds {
		if aid, err := uuid.Parse(id); err == nil {
			_ = h.q.MarkAssetUploaded(r.Context(), aid)
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"asset_ids": body.AssetIds})
}

func (h *Handler) markUploaded(w http.ResponseWriter, r *http.Request) {
	aid, err := uuid.Parse(chi.URLParam(r, "asset_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	if _, err := h.q.GetAsset(r.Context(), aid); err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.MarkAssetUploaded(r.Context(), aid)
	w.WriteHeader(http.StatusNoContent)
}

// serve streams a stored asset (public: used directly as an <img src>).
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	aid, err := uuid.Parse(chi.URLParam(r, "asset_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := h.q.GetAsset(r.Context(), aid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.cfg.AssetDir, aid.String())
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", a.ContentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = io.Copy(w, f)
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	if aid, err := uuid.Parse(chi.URLParam(r, "asset_id")); err == nil {
		_ = os.Remove(filepath.Join(h.cfg.AssetDir, aid.String()))
	}
	w.WriteHeader(http.StatusNoContent)
}

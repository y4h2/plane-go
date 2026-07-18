package issue

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

// RoutesActions registers archive/bulk endpoints (issue-list level).
func (h *Handler) RoutesActions(r chi.Router) {
	base := "/workspaces/{slug}/projects/{project_id}"
	r.Post(base+"/issues/{issue_id}/archive/", h.archive)
	r.Delete(base+"/issues/{issue_id}/archive/", h.unarchive)
	r.Delete(base+"/bulk-delete-issues/", h.bulkDelete)
	r.Post(base+"/bulk-archive-issues/", h.bulkArchive)
}

var relationInverse = map[string]string{
	"blocking": "blocked_by", "blocked_by": "blocking",
	"duplicate": "duplicate", "relates_to": "relates_to",
	"start_after": "start_before", "start_before": "start_after",
	"finish_after": "finish_before", "finish_before": "finish_after",
}

func archivableState(group string) bool {
	return group == "completed" || group == "cancelled"
}

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	grp, _ := h.q.StateGroupForIssue(ctx, iid)
	if !archivableState(grp) {
		httpx.JSON(w, http.StatusBadRequest, map[string]any{"error": "Can only archive completed or cancelled state group issue"})
		return
	}
	_ = h.q.ArchiveIssue(ctx, gen.ArchiveIssueParams{ID: iid, ProjectID: pid})
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": time.Now().UTC()})
}

func (h *Handler) unarchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	_ = h.q.UnarchiveIssue(ctx, gen.UnarchiveIssueParams{ID: iid, ProjectID: pid})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) bulkDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	ids := h.parseIssueIDs(r)
	_ = h.q.BulkSoftDeleteIssues(ctx, gen.BulkSoftDeleteIssuesParams{ProjectID: pid, Column2: ids})
	httpx.JSON(w, http.StatusOK, map[string]any{"message": itoa(len(ids)) + " issues were deleted"})
}

func (h *Handler) bulkArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	ids := h.parseIssueIDs(r)
	// all target issues must be in a completed/cancelled state group
	for _, id := range ids {
		grp, _ := h.q.StateGroupForIssue(ctx, id)
		if !archivableState(grp) {
			httpx.JSON(w, http.StatusBadRequest, map[string]any{"error_code": 4091, "error_message": "INVALID_ARCHIVE_STATE_GROUP"})
			return
		}
	}
	for _, id := range ids {
		_ = h.q.ArchiveIssue(ctx, gen.ArchiveIssueParams{ID: id, ProjectID: pid})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"archived_at": time.Now().UTC()})
}

// ---- sub-issues (write) ----------------------------------------------------

func (h *Handler) addSubIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	var body struct {
		SubIssueIDs []string `json:"sub_issue_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, id := range body.SubIssueIDs {
		if sid, err := uuid.Parse(id); err == nil {
			_ = h.q.SetIssueParent(ctx, gen.SetIssueParentParams{ID: sid, ProjectID: pid, ParentID: dbx.PgUUID(iid)})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sub_issues": h.subIssueValues(ctx, iid)})
}

func (h *Handler) subIssueValues(ctx context.Context, parent uuid.UUID) []map[string]any {
	kids, _ := h.q.ListSubIssues(ctx, dbx.PgUUID(parent))
	out := make([]map[string]any, 0, len(kids))
	for _, k := range kids {
		out = append(out, Values(k))
	}
	return out
}

// ---- relations (write + real read) -----------------------------------------

func (h *Handler) addRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	var body struct {
		RelationType string   `json:"relation_type"`
		Issues       []string `json:"issues"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	inv := relationInverse[body.RelationType]
	for _, id := range body.Issues {
		rid, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		_ = h.q.CreateRelation(ctx, gen.CreateRelationParams{WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, RelatedIssueID: rid, RelationType: body.RelationType})
		if inv != "" {
			_ = h.q.CreateRelation(ctx, gen.CreateRelationParams{WorkspaceID: ws.ID, ProjectID: pid, IssueID: rid, RelatedIssueID: iid, RelationType: inv})
		}
	}
	httpx.JSON(w, http.StatusCreated, []any{})
}

func (h *Handler) removeRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, _, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	var body struct {
		RelationType string `json:"relation_type"`
		RelatedIssue string `json:"related_issue"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if rid, err := uuid.Parse(body.RelatedIssue); err == nil {
		_ = h.q.DeleteRelation(ctx, gen.DeleteRelationParams{IssueID: iid, RelatedIssueID: rid, RelationType: body.RelationType})
		if inv := relationInverse[body.RelationType]; inv != "" {
			_ = h.q.DeleteRelation(ctx, gen.DeleteRelationParams{IssueID: rid, RelatedIssueID: iid, RelationType: inv})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- attachments (list; upload goes through the asset store) ----------------

func (h *Handler) listAttachments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	rows, _ := h.q.ListIssueAttachments(ctx, iid.String())
	out := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		out = append(out, map[string]any{
			"id":         a.ID.String(),
			"asset":      "/api/assets/v2/static/" + a.ID.String() + "/",
			"attributes": map[string]any{"name": a.Name, "size": a.Size, "type": a.ContentType},
			"issue_id":   iid.String(),
			"created_at": a.CreatedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- helpers ---------------------------------------------------------------

func (h *Handler) parseIssueIDs(r *http.Request) []uuid.UUID {
	var body struct {
		IssueIDs []string `json:"issue_ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out := make([]uuid.UUID, 0, len(body.IssueIDs))
	for _, id := range body.IssueIDs {
		if u, err := uuid.Parse(id); err == nil {
			out = append(out, u)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

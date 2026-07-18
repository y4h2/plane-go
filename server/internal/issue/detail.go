package issue

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"planego/internal/auth"
	"planego/internal/db/gen"
	"planego/internal/dbx"
	"planego/internal/httpx"
)

// RoutesDetail registers the issue sub-resource endpoints (comments, links,
// sub-issues). Kept separate from the core issue routes for readability.
func (h *Handler) RoutesDetail(r chi.Router) {
	base := "/workspaces/{slug}/projects/{project_id}/issues/{issue_id}"
	r.Post(base+"/comments/", h.addComment)
	r.Get(base+"/comments/", h.listComments)
	r.Delete(base+"/comments/{comment_id}/", h.deleteComment)
	r.Post(base+"/issue-links/", h.addLink)
	r.Get(base+"/issue-links/", h.listLinks)
	r.Delete(base+"/issue-links/{link_id}/", h.deleteLink)
	r.Get(base+"/sub-issues/", h.subIssues)
	r.Get(base+"/subscribe/", h.getSubscribe)
	r.Post(base+"/subscribe/", h.postSubscribe)
	r.Delete(base+"/subscribe/", h.deleteSubscribe)
	r.Get(base+"/issue-subscribers/", h.listSubscribers)
	r.Get(base+"/reactions/", h.listReactions)
	r.Post(base+"/reactions/", h.addReaction)
}

func (h *Handler) getSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	sub, _ := h.q.IsSubscribed(ctx, gen.IsSubscribedParams{IssueID: iid, SubscriberID: u.ID})
	httpx.JSON(w, http.StatusOK, map[string]any{"subscribed": sub})
}

func (h *Handler) postSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	if sub, _ := h.q.IsSubscribed(ctx, gen.IsSubscribedParams{IssueID: iid, SubscriberID: u.ID}); sub {
		httpx.JSON(w, http.StatusBadRequest, map[string]string{"message": "User already subscribed to the issue."})
		return
	}
	_ = h.q.Subscribe(ctx, gen.SubscribeParams{WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, SubscriberID: u.ID})
	httpx.JSON(w, http.StatusCreated, map[string]any{"subscribed": true})
}

func (h *Handler) deleteSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	_ = h.q.Unsubscribe(ctx, gen.UnsubscribeParams{IssueID: iid, SubscriberID: u.ID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listSubscribers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	rows, err := h.q.ListSubscribers(ctx, iid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, map[string]any{
			"id": s.ID.String(),
			"member": map[string]any{
				"id": s.SubscriberID.String(), "display_name": s.DisplayName,
				"first_name": s.FirstName, "last_name": s.LastName,
				"avatar": s.Avatar, "avatar_url": nil, "is_bot": s.IsBot, "email": s.Email,
			},
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func reactionResp(re gen.IssueReaction) map[string]any {
	return map[string]any{
		"id":         re.ID.String(),
		"reaction":   re.Reaction,
		"actor":      dbx.StrOrEmpty(re.ActorID),
		"issue":      re.IssueID.String(),
		"project":    re.ProjectID.String(),
		"workspace":  re.WorkspaceID.String(),
		"created_at": re.CreatedAt,
	}
}

func (h *Handler) listReactions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	rows, err := h.q.ListReactions(ctx, iid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, re := range rows {
		out = append(out, reactionResp(re))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		Reaction string `json:"reaction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reaction == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"reaction": {"This field is required."}})
		return
	}
	re, err := h.q.CreateReaction(ctx, gen.CreateReactionParams{
		WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, ActorID: dbx.PgUUID(u.ID), Reaction: body.Reaction,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, reactionResp(re))
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	return strings.TrimSpace(tagRe.ReplaceAllString(s, ""))
}

func commentResp(c gen.IssueComment) map[string]any {
	return map[string]any{
		"id":               c.ID.String(),
		"comment_html":     c.CommentHtml,
		"comment_stripped": c.CommentStripped,
		"actor":            dbx.StrOrEmpty(c.ActorID),
		"issue":            c.IssueID.String(),
		"project":          c.ProjectID.String(),
		"workspace":        c.WorkspaceID.String(),
		"created_at":       c.CreatedAt,
		"updated_at":       c.UpdatedAt,
	}
}

func linkResp(l gen.IssueLink) map[string]any {
	return map[string]any{
		"id":         l.ID.String(),
		"url":        l.Url,
		"title":      l.Title,
		"issue":      l.IssueID.String(),
		"project":    l.ProjectID.String(),
		"workspace":  l.WorkspaceID.String(),
		"created_by": dbx.StrPtr(l.CreatedBy),
		"created_at": l.CreatedAt,
		"updated_at": l.UpdatedAt,
	}
}

func (h *Handler) addComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		CommentHTML string `json:"comment_html"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c, err := h.q.CreateComment(ctx, gen.CreateCommentParams{
		WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, ActorID: dbx.PgUUID(u.ID),
		CommentHtml: body.CommentHTML, CommentStripped: stripHTML(body.CommentHTML),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, commentResp(c))
}

func (h *Handler) listComments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	rows, err := h.q.ListComments(ctx, iid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, commentResp(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	cid, err := uuid.Parse(chi.URLParam(r, "comment_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteComment(ctx, gen.DeleteCommentParams{ID: cid, IssueID: iid})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) addLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, pid, ok := h.scope(ctx, w, r)
	if !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(ctx)
	var body struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		httpx.JSON(w, http.StatusBadRequest, map[string][]string{"url": {"This field is required."}})
		return
	}
	l, err := h.q.CreateLink(ctx, gen.CreateLinkParams{
		WorkspaceID: ws.ID, ProjectID: pid, IssueID: iid, Url: body.URL, Title: body.Title, CreatedBy: dbx.PgUUID(u.ID),
	})
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "The payload is not valid")
		return
	}
	httpx.JSON(w, http.StatusCreated, linkResp(l))
}

func (h *Handler) listLinks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	rows, err := h.q.ListLinks(ctx, iid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The required object does not exist.")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, l := range rows {
		out = append(out, linkResp(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	iid, ok := parseIssueID(w, r)
	if !ok {
		return
	}
	lid, err := uuid.Parse(chi.URLParam(r, "link_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return
	}
	_ = h.q.DeleteLink(ctx, gen.DeleteLinkParams{ID: lid, IssueID: iid})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) subIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, _, ok := h.scope(ctx, w, r); !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"sub_issues":         []any{},
		"state_distribution": map[string]any{},
	})
}

func parseIssueID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	iid, err := uuid.Parse(chi.URLParam(r, "issue_id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "The required object does not exist.")
		return uuid.UUID{}, false
	}
	return iid, true
}

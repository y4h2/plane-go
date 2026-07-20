// Package activity records and reads the issue activity log — the audit trail
// of who created/changed what on an issue. In Plane's Django backend this is
// written by the `issue_activity` Celery task (invoked from 78 call sites); here
// the write runs in a bg.Dispatcher goroutine off the request path, and the
// issues/{id}/history/ endpoint reads it back.
package activity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"planego/internal/dbx"
)

// Entry is one activity row to persist.
type Entry struct {
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	IssueID     uuid.UUID
	ActorID     uuid.UUID
	Verb        string // "created" | "updated"
	Field       string // "" for the create entry, else "name"/"priority"/"state"/...
	OldValue    string
	NewValue    string
	Comment     string
}

// Record persists one activity row. Safe to call from a background goroutine;
// errors are returned (the caller logs) but never surface to the HTTP request.
func Record(ctx context.Context, pool *pgxpool.Pool, e Entry) error {
	_, err := pool.Exec(ctx, `
		insert into issue_activities
		    (workspace_id, project_id, issue_id, actor_id, verb, field, old_value, new_value, comment, epoch)
		values ($1,$2,$3,$4,$5,nullif($6,''),nullif($7,''),nullif($8,''),$9,$10)`,
		e.WorkspaceID, e.ProjectID, e.IssueID, e.ActorID, e.Verb, e.Field, e.OldValue, e.NewValue, e.Comment,
		float64(time.Now().UnixNano())/1e9)
	return err
}

// History returns an issue's activity log, newest-last (created order), in the
// IssueActivitySerializer wire shape the frontend renders.
func History(ctx context.Context, pool *pgxpool.Pool, issueID uuid.UUID) ([]map[string]any, error) {
	rows, err := pool.Query(ctx, `
		select a.id, a.verb, a.field, a.old_value, a.new_value, a.comment,
		       a.actor_id, a.issue_id, a.project_id, a.workspace_id,
		       a.old_identifier, a.new_identifier, a.epoch, a.created_at,
		       u.display_name, u.avatar
		from issue_activities a
		left join users u on u.id = a.actor_id
		where a.issue_id = $1
		order by a.created_at asc, a.epoch asc`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, issueIDc, projectID, workspaceID uuid.UUID
			actorID, oldIdent, newIdent          pgtype.UUID
			verb                                 string
			field, oldVal, newVal, comment       *string
			epoch                                *float64
			createdAt                            time.Time
			displayName, avatar                  *string
		)
		if err := rows.Scan(&id, &verb, &field, &oldVal, &newVal, &comment,
			&actorID, &issueIDc, &projectID, &workspaceID,
			&oldIdent, &newIdent, &epoch, &createdAt, &displayName, &avatar); err != nil {
			continue
		}
		var actorDetail any
		if actorID.Valid {
			actorDetail = map[string]any{
				"id":           dbx.StrOrEmpty(actorID),
				"display_name": strOr(displayName, ""),
				"avatar":       strOr(avatar, ""),
			}
		}
		out = append(out, map[string]any{
			"id":             id.String(),
			"verb":           verb,
			"field":          field,
			"old_value":      oldVal,
			"new_value":      newVal,
			"comment":        strOr(comment, ""),
			"attachments":    []any{},
			"actor":          dbx.StrOrEmpty(actorID),
			"actor_detail":   actorDetail,
			"issue":          issueIDc.String(),
			"issue_detail":   nil,
			"project":        projectID.String(),
			"workspace":      workspaceID.String(),
			"old_identifier": dbx.StrOrEmpty(oldIdent),
			"new_identifier": dbx.StrOrEmpty(newIdent),
			"epoch":          epoch,
			"created_at":     createdAt,
			"updated_at":     createdAt,
		})
	}
	return out, rows.Err()
}

func strOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

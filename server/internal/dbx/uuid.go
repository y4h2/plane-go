// Package dbx holds small helpers for converting between pgx and app types.
package dbx

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// PgUUID wraps a uuid.UUID as a non-null pgtype.UUID (for nullable columns).
func PgUUID(u uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: u, Valid: true}
}

// NullUUID is a NULL pgtype.UUID.
func NullUUID() pgtype.UUID { return pgtype.UUID{} }

// StrOrEmpty renders a nullable pgtype.UUID as a string ("" when NULL).
func StrOrEmpty(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

// StrPtr renders a nullable pgtype.UUID as *string (nil when NULL) for JSON.
func StrPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuid.UUID(u.Bytes).String()
	return &s
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chu/3xui-user-sync/internal/domain"
)

var ErrNotFound = errors.New("not found")

type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) List(ctx context.Context) ([]domain.User, error) {
	return r.ListFiltered(ctx, "", "username", "asc")
}

func (r *UserRepository) ListFiltered(ctx context.Context, search, sortBy, sortDir string) ([]domain.User, error) {
	orderBy := "username"
	switch sortBy {
	case "subscription_id":
		orderBy = "subscription_id"
	}
	dir := "ASC"
	if strings.EqualFold(sortDir, "desc") {
		dir = "DESC"
	}

	query := `
		SELECT id, username, subscription_id, uid, created_at, updated_at
		FROM users`
	args := []any{}
	search = strings.TrimSpace(search)
	if search != "" {
		query += ` WHERE username LIKE ? OR subscription_id LIKE ?`
		pattern := "%" + search + "%"
		args = append(args, pattern, pattern)
	}
	query += fmt.Sprintf(` ORDER BY %s %s, id ASC`, orderBy, dir)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.User
	for rows.Next() {
		var item domain.User
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Username, &item.SubscriptionID, &item.UID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *UserRepository) GetByID(ctx context.Context, id int64) (domain.User, error) {
	var item domain.User
	var createdAt, updatedAt string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, username, subscription_id, uid, created_at, updated_at
		FROM users WHERE id = ?`, id).
		Scan(&item.ID, &item.Username, &item.SubscriptionID, &item.UID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return item, nil
}

func (r *UserRepository) GetByUID(ctx context.Context, uid string) (domain.User, error) {
	var item domain.User
	var createdAt, updatedAt string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, username, subscription_id, uid, created_at, updated_at
		FROM users WHERE uid = ?`, uid).
		Scan(&item.ID, &item.Username, &item.SubscriptionID, &item.UID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return item, nil
}

func (r *UserRepository) Create(ctx context.Context, user domain.User) (domain.User, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO users (username, subscription_id, uid, created_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		strings.TrimSpace(user.Username), strings.TrimSpace(user.SubscriptionID), strings.TrimSpace(user.UID),
	)
	if err != nil {
		return domain.User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.User{}, err
	}
	return r.GetByID(ctx, id)
}

func (r *UserRepository) Update(ctx context.Context, user domain.User) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE users
		SET username = ?, subscription_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		strings.TrimSpace(user.Username), strings.TrimSpace(user.SubscriptionID), user.ID,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *UserRepository) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

type ServerRepository struct {
	db *sql.DB
}

func NewServerRepository(db *sql.DB) *ServerRepository {
	return &ServerRepository{db: db}
}

func (r *ServerRepository) List(ctx context.Context) ([]domain.Server, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, base_url, panel_username, panel_password_enc, subscription_url, active, created_at, updated_at
		FROM servers
		ORDER BY COALESCE(NULLIF(name, ''), base_url) ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Server
	for rows.Next() {
		var item domain.Server
		var createdAt, updatedAt string
		var active int
		if err := rows.Scan(&item.ID, &item.Name, &item.BaseURL, &item.PanelUsername, &item.PanelPasswordEnc, &item.SubscriptionURL, &active, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.Active = active == 1
		item.CreatedAt = parseSQLiteTime(createdAt)
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ServerRepository) GetByID(ctx context.Context, id int64) (domain.Server, error) {
	var item domain.Server
	var createdAt, updatedAt string
	var active int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, base_url, panel_username, panel_password_enc, subscription_url, active, created_at, updated_at
		FROM servers WHERE id = ?`, id).
		Scan(&item.ID, &item.Name, &item.BaseURL, &item.PanelUsername, &item.PanelPasswordEnc, &item.SubscriptionURL, &active, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, ErrNotFound
	}
	if err != nil {
		return domain.Server{}, err
	}
	item.Active = active == 1
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return item, nil
}

func (r *ServerRepository) Create(ctx context.Context, server domain.Server) (domain.Server, error) {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO servers (name, base_url, panel_username, panel_password_enc, subscription_url, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		strings.TrimSpace(server.Name),
		strings.TrimSpace(server.BaseURL),
		strings.TrimSpace(server.PanelUsername),
		server.PanelPasswordEnc,
		strings.TrimSpace(server.SubscriptionURL),
		boolToInt(server.Active),
	)
	if err != nil {
		return domain.Server{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Server{}, err
	}
	return r.GetByID(ctx, id)
}

func (r *ServerRepository) Update(ctx context.Context, server domain.Server) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE servers
		SET name = ?, base_url = ?, panel_username = ?, panel_password_enc = ?, subscription_url = ?, active = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		strings.TrimSpace(server.Name),
		strings.TrimSpace(server.BaseURL),
		strings.TrimSpace(server.PanelUsername),
		server.PanelPasswordEnc,
		strings.TrimSpace(server.SubscriptionURL),
		boolToInt(server.Active),
		server.ID,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ServerRepository) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func parseSQLiteTime(v string) time.Time {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

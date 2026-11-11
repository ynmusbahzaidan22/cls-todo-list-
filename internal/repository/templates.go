package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type TemplateRepository struct {
	pool *pgxpool.Pool
}

func NewTemplateRepository(pool *pgxpool.Pool) *TemplateRepository {
	return &TemplateRepository{pool: pool}
}

func (r *TemplateRepository) CreateTemplate(ctx context.Context, userID, name, description string, items []task.TemplateItem) (task.Template, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Template{}, err
	}
	defer tx.Rollback(ctx)

	template, err := scanTemplate(tx.QueryRow(ctx, `
		INSERT INTO app.task_templates (user_id, name, description)
		VALUES ($1, $2, NULLIF($3, ''))
		RETURNING id::text, name, COALESCE(description, ''), version, created_at, updated_at`, userID, name, description))
	if err != nil {
		return task.Template{}, err
	}
	if err := insertTemplateItems(ctx, tx, userID, template.ID, items); err != nil {
		return task.Template{}, err
	}
	template.Items, err = listTemplateItems(ctx, tx, userID, template.ID)
	if err != nil {
		return task.Template{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Template{}, err
	}
	return template, nil
}

func (r *TemplateRepository) ListTemplates(ctx context.Context, userID string) ([]task.TemplateSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.id::text, t.name, COALESCE(t.description, ''), t.version,
		       (SELECT count(*)::int FROM app.task_template_items i
		        WHERE i.template_id = t.id AND i.user_id = t.user_id),
		       t.created_at, t.updated_at
		FROM app.task_templates t
		WHERE t.user_id = $1
		ORDER BY t.updated_at DESC, t.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]task.TemplateSummary, 0)
	for rows.Next() {
		var item task.TemplateSummary
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Version, &item.ItemCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *TemplateRepository) GetTemplate(ctx context.Context, userID, templateID string) (task.Template, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return task.Template{}, err
	}
	defer tx.Rollback(ctx)

	template, err := scanTemplate(tx.QueryRow(ctx, `
		SELECT id::text, name, COALESCE(description, ''), version, created_at, updated_at
		FROM app.task_templates WHERE id = $1 AND user_id = $2`, templateID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Template{}, task.ErrNotFound
	}
	if err != nil {
		return task.Template{}, err
	}
	template.Items, err = listTemplateItems(ctx, tx, userID, templateID)
	if err != nil {
		return task.Template{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Template{}, err
	}
	return template, nil
}

func (r *TemplateRepository) UpdateTemplate(ctx context.Context, userID, templateID string, version int64, name, description string, items []task.TemplateItem) (task.Template, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Template{}, err
	}
	defer tx.Rollback(ctx)

	current, err := lockTemplate(ctx, tx, userID, templateID, true)
	if err != nil {
		return task.Template{}, err
	}
	if current.Version != version {
		return task.Template{}, task.ErrConflict
	}
	updated, err := scanTemplate(tx.QueryRow(ctx, `
		UPDATE app.task_templates
		SET name = $4, description = NULLIF($5, ''), version = version + 1, updated_at = clock_timestamp()
		WHERE id = $1 AND user_id = $2 AND version = $3
		RETURNING id::text, name, COALESCE(description, ''), version, created_at, updated_at`,
		templateID, userID, version, name, description))
	if err != nil {
		return task.Template{}, err
	}
	existingItems, err := listTemplateItems(ctx, tx, userID, templateID)
	if err != nil {
		return task.Template{}, err
	}
	existing := make(map[string]struct{}, len(existingItems))
	for _, item := range existingItems {
		existing[item.ID] = struct{}{}
	}
	retained := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if _, ok := existing[item.ID]; !ok {
			return task.Template{}, task.ErrInvalidInput
		}
		retained[item.ID] = struct{}{}
	}
	for _, item := range existingItems {
		if _, ok := retained[item.ID]; !ok {
			if _, err := tx.Exec(ctx, `DELETE FROM app.task_template_items WHERE id = $1 AND template_id = $2 AND user_id = $3`, item.ID, templateID, userID); err != nil {
				return task.Template{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE app.task_template_items
		SET title = 'template-pending-' || gen_random_uuid()::text
		WHERE template_id = $1 AND user_id = $2`, templateID, userID); err != nil {
		return task.Template{}, err
	}
	for position, item := range items {
		if item.ID == "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO app.task_template_items (template_id, user_id, position, title, description, priority, due_in_days)
				VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)`, templateID, userID, position, item.Title, item.Description, item.Priority, item.DueInDays); err != nil {
				return task.Template{}, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE app.task_template_items
			SET position = $4, title = $5, description = NULLIF($6, ''), priority = $7, due_in_days = $8
			WHERE id = $1 AND template_id = $2 AND user_id = $3`, item.ID, templateID, userID, position, item.Title, item.Description, item.Priority, item.DueInDays); err != nil {
			return task.Template{}, err
		}
	}
	updated.Items, err = listTemplateItems(ctx, tx, userID, templateID)
	if err != nil {
		return task.Template{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Template{}, err
	}
	return updated, nil
}

func (r *TemplateRepository) DeleteTemplate(ctx context.Context, userID, templateID string, version int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	current, err := lockTemplate(ctx, tx, userID, templateID, true)
	if err != nil {
		return err
	}
	if current.Version != version {
		return task.ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.task_templates WHERE id = $1 AND user_id = $2 AND version = $3`, templateID, userID, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *TemplateRepository) ApplyTemplate(ctx context.Context, userID, templateID string, version int64, newSet bool, timeZones ...string) ([]task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	current, err := lockTemplate(ctx, tx, userID, templateID, true)
	if err != nil {
		return nil, err
	}
	if current.Version != version {
		return nil, task.ErrConflict
	}
	items, err := listTemplateItems(ctx, tx, userID, templateID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, task.ErrInvalidInput
	}
	var applicationID string
	if !newSet {
		var latest pgtype.Text
		if err := tx.QueryRow(ctx, `SELECT latest_application_id::text FROM app.task_templates WHERE id = $1 AND user_id = $2`, templateID, userID).Scan(&latest); err != nil {
			return nil, err
		}
		if latest.Valid {
			applicationID = latest.String
		}
	}
	if applicationID == "" {
		if err := tx.QueryRow(ctx, `INSERT INTO app.task_template_applications (template_id, user_id) VALUES ($1, $2) RETURNING id::text`, templateID, userID).Scan(&applicationID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE app.task_templates SET latest_application_id = $3 WHERE id = $1 AND user_id = $2`, templateID, userID, applicationID); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT template_item_id::text FROM app.task_template_application_items WHERE application_id = $1`, applicationID)
	if err != nil {
		return nil, err
	}
	appliedItems := make(map[string]struct{}, len(items))
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return nil, err
		}
		appliedItems[itemID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var baseDate string
	var dateErr error
	if len(timeZones) > 0 && timeZones[0] != "" {
		dateErr = tx.QueryRow(ctx, `SELECT to_char((clock_timestamp() AT TIME ZONE $2::text)::date, 'YYYY-MM-DD') FROM app.users WHERE id = $1`, userID, timeZones[0]).Scan(&baseDate)
	} else {
		dateErr = tx.QueryRow(ctx, `SELECT to_char((clock_timestamp() AT TIME ZONE COALESCE(time_zone, 'UTC'))::date, 'YYYY-MM-DD') FROM app.users WHERE id = $1`, userID).Scan(&baseDate)
	}
	if dateErr != nil {
		return nil, dateErr
	}
	base, err := time.Parse("2006-01-02", baseDate)
	if err != nil {
		return nil, err
	}
	created := make([]task.Task, 0, len(items))
	for _, item := range items {
		if _, exists := appliedItems[item.ID]; exists {
			continue
		}
		var due any
		if item.DueInDays != nil {
			due = base.AddDate(0, 0, *item.DueInDays).Format("2006-01-02")
		}
		createdTask, err := insertTaskTx(ctx, tx, userID, item.Title, item.Description, task.StatusTODO, item.Priority, due)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.task_template_application_items (application_id, template_id, user_id, template_item_id, task_id)
			VALUES ($1, $2, $3, $4, $5)`, applicationID, templateID, userID, item.ID, createdTask.ID); err != nil {
			return nil, err
		}
		created = append(created, createdTask)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func lockTemplate(ctx context.Context, tx pgx.Tx, userID, templateID string, forUpdate bool) (task.Template, error) {
	// The lock level is selected from the internal call site, never from request data.
	query := `SELECT id::text, name, COALESCE(description, ''), version, created_at, updated_at
	          FROM app.task_templates WHERE id = $1 AND user_id = $2 FOR SHARE`
	if forUpdate {
		query = `SELECT id::text, name, COALESCE(description, ''), version, created_at, updated_at
		         FROM app.task_templates WHERE id = $1 AND user_id = $2 FOR UPDATE`
	}
	template, err := scanTemplate(tx.QueryRow(ctx, query, templateID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Template{}, task.ErrNotFound
	}
	return template, err
}

func insertTemplateItems(ctx context.Context, tx pgx.Tx, userID, templateID string, items []task.TemplateItem) error {
	for position, item := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app.task_template_items (template_id, user_id, position, title, description, priority, due_in_days)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)`, templateID, userID, position, item.Title, item.Description, item.Priority, item.DueInDays); err != nil {
			return err
		}
	}
	return nil
}

func listTemplateItems(ctx context.Context, tx pgx.Tx, userID, templateID string) ([]task.TemplateItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, title, COALESCE(description, ''), priority, due_in_days
		FROM app.task_template_items
		WHERE template_id = $1 AND user_id = $2
		ORDER BY position`, templateID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]task.TemplateItem, 0)
	for rows.Next() {
		var item task.TemplateItem
		var dueInDays pgtype.Int4
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Priority, &dueInDays); err != nil {
			return nil, err
		}
		if dueInDays.Valid {
			value := int(dueInDays.Int32)
			item.DueInDays = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanTemplate(row rowScanner) (task.Template, error) {
	var template task.Template
	err := row.Scan(&template.ID, &template.Name, &template.Description, &template.Version, &template.CreatedAt, &template.UpdatedAt)
	return template, err
}

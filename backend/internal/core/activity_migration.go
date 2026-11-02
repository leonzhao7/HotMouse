package core

import "database/sql"

func migrateActivitySchema(db *sql.DB) error {
	auditColumns := map[string]string{
		"actor":       `TEXT NOT NULL DEFAULT 'system'`,
		"target_type": `TEXT`,
		"target_id":   `TEXT`,
		"summary":     `TEXT NOT NULL DEFAULT ''`,
		"detail":      `TEXT`,
		"user_id":     `TEXT`,
		"resource":    `TEXT`,
		"detail_json": `TEXT NOT NULL DEFAULT '{}'`,
	}
	for name, definition := range auditColumns {
		if err := addColumnIfMissing(db, "audit_log", name, definition); err != nil {
			return err
		}
	}
	notificationColumns := map[string]string{
		"level":   `TEXT NOT NULL DEFAULT 'info'`,
		"body":    `TEXT`,
		"link":    `TEXT`,
		"read":    `INTEGER NOT NULL DEFAULT 0`,
		"user_id": `TEXT`,
		"kind":    `TEXT`,
		"read_at": `TEXT`,
	}
	for name, definition := range notificationColumns {
		if err := addColumnIfMissing(db, "notifications", name, definition); err != nil {
			return err
		}
	}
	_, err := db.Exec(`UPDATE audit_log SET summary=COALESCE(NULLIF(summary,''),action),target_type=COALESCE(target_type,'api'),target_id=COALESCE(target_id,resource),detail=COALESCE(detail,detail_json),actor=COALESCE(NULLIF(actor,''),'system')`)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE notifications SET level=COALESCE(NULLIF(level,''),kind,'info'),read=CASE WHEN read_at IS NULL THEN COALESCE(read,0) ELSE 1 END`)
	return err
}

func addColumnIfMissing(db *sql.DB, table, name, definition string) error {
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
}

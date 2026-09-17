package store

import (
	"database/sql"
	"fmt"
)

type Attachment struct {
	ID         int64  `json:"id"`
	CardID     int64  `json:"card_id"`
	Kind       string `json:"kind"`
	OriginName string `json:"origin_name"`
	URL        string `json:"url"`
	SizeBytes  int64  `json:"size_bytes"`
}

func (s *Store) SaveAttachment(cardID int64, kind, originName, storedPath string, size int64, by string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO asset_attachment (card_id, kind, origin_name, stored_path, size_bytes, uploaded_by)
		VALUES (?, ?, ?, ?, ?, ?)`, cardID, kind, originName, storedPath, size, by)
	if err != nil {
		return 0, fmt.Errorf("insert attachment: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) ListAttachments(cardID int64) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT id, card_id, kind, origin_name, stored_path, size_bytes
		FROM asset_attachment WHERE card_id = ? ORDER BY id`, cardID)
	if err != nil {
		return nil, fmt.Errorf("query attachments: %w", err)
	}
	defer rows.Close()

	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		var stored string
		if err := rows.Scan(&a.ID, &a.CardID, &a.Kind, &a.OriginName, &stored, &a.SizeBytes); err != nil {
			return nil, err
		}
		a.URL = "/uploads/" + stored
		out = append(out, a)
	}
	return out, rows.Err()
}

// BindAttachments 新建资产时先上传（card_id=0），保存后再回填
func (s *Store) BindAttachments(cardID int64, ids []int64) error {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, err := s.db.Exec("UPDATE asset_attachment SET card_id = ? WHERE id = ? AND card_id = 0", cardID, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteAttachment 返回落盘文件名，交由调用方删除物理文件
func (s *Store) DeleteAttachment(id int64) (string, error) {
	var stored string
	err := s.db.QueryRow("SELECT stored_path FROM asset_attachment WHERE id = ?", id).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec("DELETE FROM asset_attachment WHERE id = ?", id); err != nil {
		return "", err
	}
	return stored, nil
}

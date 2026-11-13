package notifications

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode"
)

type premiereNotice struct {
	Source    string `json:"source"`
	EventID   string `json:"event_id"`
	Title     string `json:"title"`
	Message   string `json:"message,omitempty"`
	URL       string `json:"url,omitempty"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

func validPremiereURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") &&
		u.Scheme == "" && u.Host == "" && u.User == nil && u.Opaque == "" &&
		!strings.HasPrefix(u.Path, "//") && !strings.ContainsAny(raw, "\\") &&
		!strings.ContainsAny(u.Path, "\\") && !strings.ContainsFunc(u.Path, unicode.IsControl)
}

func premiereFlags(row DeliveryRow) premiereNotice {
	var flags premiereNotice
	if row.Type == DeliveryTypePremiere {
		_ = json.Unmarshal(row.ReasonFlags, &flags)
	}
	return flags
}

func deliveryExpired(row DeliveryRow, now time.Time) bool {
	flags := premiereFlags(row)
	return flags.ExpiresAt != nil && now.UnixMilli() >= *flags.ExpiresAt
}

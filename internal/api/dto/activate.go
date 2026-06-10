package dto

// ActivateResponse is the body of POST /api/ship/{id}/activate (phase 10.14a).
type ActivateResponse struct {
	OK bool `json:"ok"`
}

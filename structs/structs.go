package structs

type Error struct {
	Reason string `json:"reason"`
	Msg    string `json:"message"`
}

type NewRegistration struct {
	FcmToken       string `json:"fcm_token"`
	SupposedUserId int    `json:"user_id"`
	IsFirebase     bool   `json:"is_firebase"`
}

type UpdateRegistration struct {
	OldFcm         string `json:"fcm_token_old"`
	NewFcmToken    string `json:"fcm_token"`
	SupposedUserId int    `json:"user_id"`
}

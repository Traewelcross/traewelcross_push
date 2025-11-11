package structs

type Error struct {
	Reason string `json:"reason"`
	Msg    string `json:"message"`
}

type NewRegistration struct {
	FcmToken       string `json:"fcm_token"`
	SupposedUserId string `json:"user_id"`
}

type UpdateRegistration struct {
	OldFcm         string `json:"fcm_token_old"`
	NewFcmToken    string `json:"fcm_token"`
	SupposedUserId string `json:"user_id"`
}

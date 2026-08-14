package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"traewelcross_push/structs"

	"github.com/rs/zerolog"
	"google.golang.org/api/option"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

var logger zerolog.Logger

func main() {
	logger = zerolog.New(
		zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339},
	).Level(zerolog.TraceLevel).With().Timestamp().Caller().Logger()
	err := godotenv.Load()
	if err != nil {
		logger.Error().Msg("Error loading .env file")
	}

	app, err := firebase.NewApp(context.Background(), nil, option.WithCredentialsFile(os.Getenv("FIREBASE_CONFIG")))
	if err != nil {
		logger.Fatal().Msgf("firebase error: %v\n", err)
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_NAME"),
		os.Getenv("DB_SSLMODE"),
	)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		logger.Fatal().Msgf("Failed to open database connection: %v", err)
	}
	defer db.Close()

	err = db.PingContext(context.Background())
	if err != nil {
		logger.Fatal().Msgf("Failed to ping database: %v", err)
	}

	log.Println("Successfully connected to the database!")

	err = createDevicesTable(db)
	if err != nil {
		logger.Fatal().Msgf("Failed to create devices table: %v", err)
	}

	err = createEventsTable(db)
	if err != nil {
		logger.Fatal().Msgf("Failed to create events table: %v", err)
	}

	log.Println("Devices & Events tables successfully created or already exists.")
	router := gin.Default()
	router.GET("/", func(c *gin.Context) {
		str, _ := io.ReadAll(c.Request.Body)
		fmt.Println(string(str))
		c.Redirect(301, "https://traewelcross.de")
	})
	router.POST("/register", func(ctx *gin.Context) {
		registerDevice(ctx, db)
	})
	router.PATCH("/update", func(ctx *gin.Context) {
		updateDevice(ctx, db)
	})
	router.DELETE("/unregister", func(ctx *gin.Context) {
		deleteDevice(ctx, db)
	})
	router.POST("/wh", func(ctx *gin.Context) {
		sendPush(ctx, db, app)
	})
	router.Run()
}

func updateDevice(ctx *gin.Context, db *sql.DB) {
	tokenFull := ctx.Request.Header.Get("Authorization")
	authToken := []byte(strings.Split(tokenFull, " ")[1])
	rawJson, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//body read error
			Reason: "bre",
			Msg:    err.Error(),
		})
		return
	}
	var registration structs.UpdateRegistration
	err = json.Unmarshal(rawJson, &registration)
	if err != nil {
		ctx.JSON(400, structs.Error{
			//json unpack failed
			Reason: "juf",
			Msg:    err.Error(),
		})
		return
	}
	userId, err := getUserId(string(authToken))
	if err != nil {
		ctx.JSON(500, structs.Error{
			//failed fetching user id
			Reason: "ffu",
			Msg:    err.Error(),
		})
		return
	}
	if userId != registration.SupposedUserId {
		ctx.JSON(400, structs.Error{
			//invalid user id
			Reason: "iui",
			Msg:    "",
		})
		return
	}
	stmt, err := db.PrepareContext(ctx, "UPDATE devices SET failed_attempts = 0, updated_at = NOW(), fcm_token = $1 WHERE fcm_token = $2 AND user_id = $3")
	if err != nil {
		ctx.JSON(500, structs.Error{
			//databse prepare fail
			Reason: "dpf",
			Msg:    err.Error(),
		})
		return
	}
	_, err = stmt.ExecContext(ctx, registration.NewFcmToken, registration.OldFcm, userId)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//database insert fail
			Reason: "dif",
			Msg:    err.Error(),
		})
		return
	}
	for i := range authToken {
		authToken[i] = 0
	}
	ctx.Status(200)
}

func deleteDevice(ctx *gin.Context, db *sql.DB) {
	tokenFull := ctx.Request.Header.Get("Authorization")
	authToken := []byte(strings.Split(tokenFull, " ")[1])
	rawJson, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//body read error
			Reason: "bre",
			Msg:    err.Error(),
		})
		return
	}
	var registration structs.NewRegistration
	err = json.Unmarshal(rawJson, &registration)
	if err != nil {
		ctx.JSON(400, structs.Error{
			//json unpack failed
			Reason: "juf",
			Msg:    err.Error(),
		})
		return
	}
	userId, err := getUserId(string(authToken))
	if err != nil {
		ctx.JSON(500, structs.Error{
			//failed fetching user id
			Reason: "ffu",
			Msg:    err.Error(),
		})
		return
	}
	if userId != registration.SupposedUserId {
		ctx.JSON(400, structs.Error{
			//invalid user id
			Reason: "iui",
			Msg:    "",
		})
		return
	}
	stmt, err := db.PrepareContext(ctx, "DELETE FROM devices WHERE fcm_token = $1 AND user_id = $2")
	if err != nil {
		ctx.JSON(500, structs.Error{
			//databse prepare fail
			Reason: "dpf",
			Msg:    err.Error(),
		})
		return
	}
	_, err = stmt.ExecContext(ctx, registration.FcmToken, userId)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//database insert fail
			Reason: "dif",
			Msg:    err.Error(),
		})
		return
	}
	for i := range authToken {
		authToken[i] = 0
	}
	ctx.Status(200)
}

func sendPush(ctx *gin.Context, db *sql.DB, app *firebase.App) {
	userIdStr := ctx.Request.Header.Get("X-Trwl-User-Id")
	if userIdStr == "" {
		logger.Error().Msg("Header missing")
		ctx.Status(400)
		return
	}
	str, _ := io.ReadAll(ctx.Request.Body)
	if len(str) == 0 {
		logger.Error().Msg("Body was empty")
		ctx.Status(400)
		return
	}
	alreadyProcessed, err := checkAndRecordEvent(string(str), db, ctx)
	if alreadyProcessed {
		ctx.Status(200)
		return
	}
	if err != nil {
		logger.Warn().Err(err).Msg("Coudln't insert Event")
	}
	stmt, err := db.PrepareContext(ctx, "SELECT fcm_token FROM devices WHERE user_id = $1 AND is_firebase")
	if err != nil {
		logger.Error().Msg("Can't prepare token query")
		ctx.Status(500)
		return
	}
	rows, err := stmt.QueryContext(ctx, userIdStr)
	if err != nil {
		logger.Error().Msg("Can't query token(s)")
		ctx.Status(500)
		return
	}
	defer rows.Close()

	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			logger.Warn().Msg("Couldn't scan token")
			continue
		}
		tokens = append(tokens, token)
	}
	if len(tokens) == 0 {
		logger.Info().Msg("No tokens for user")
		ctx.Status(200)
		return
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create message client")
		ctx.Status(500)
		return
	}
	message := &messaging.MulticastMessage{
		Data: map[string]string{
			"rawBody": string(str),
		},
		Tokens: tokens,
	}
	br, err := client.SendEachForMulticast(ctx, message)
	if err != nil {
		ctx.Status(500)
		logger.Error().Msg("Failed to send push notification (general)")
		return
	}
	for i, resp := range br.Responses {
		token := tokens[i]
		if !resp.Success {
			logger.Error().Err(resp.Error).Msg("Failed to send push notification (specific token)")
			updateSQL := `UPDATE devices SET failed_attempts = failed_attempts+1, updated_at = NOW() WHERE fcm_token = $1`
			db.ExecContext(ctx, updateSQL, token)
		} else {
			updateSQL := `UPDATE devices SET failed_attempts = 0, updated_at = NOW() WHERE fcm_token = $1 AND failed_attempts > 0`
			db.ExecContext(ctx, updateSQL, token)
		}
	}
	ctx.Status(200)
}

func checkAndRecordEvent(notification string, db *sql.DB, ctx context.Context) (bool, error) {
	var eventData struct {
		Notification struct {
			ID string `json:"id"`
		} `json:"notification"`
	}
	if err := json.Unmarshal([]byte(notification), &eventData); err != nil {
		return false, fmt.Errorf("failed to get notify UUID: %w", err)
	}

	eventUUID := eventData.Notification.ID
	if eventUUID == "" {
		return false, errors.New("event UUID is missing from the request body")
	}

	stmt, _ := db.PrepareContext(ctx, "INSERT INTO processed_events (event_uuid) VALUES ($1)")
	_, err := stmt.ExecContext(ctx, eventUUID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return true, nil // It's a duplicate, but not an error
		}
		// For any other database error, it's a real problem
		return false, err
	}

	// If we get here, the insert was successful, so it's not a duplicate
	return false, nil
}

func registerDevice(ctx *gin.Context, db *sql.DB) {
	tokenFull := ctx.Request.Header.Get("Authorization")
	authToken := []byte(strings.Split(tokenFull, " ")[1])
	rawJson, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//body read error
			Reason: "bre",
			Msg:    err.Error(),
		})
		return
	}
	var registration structs.NewRegistration
	err = json.Unmarshal(rawJson, &registration)
	if err != nil {
		ctx.JSON(400, structs.Error{
			//json unpack failed
			Reason: "juf",
			Msg:    err.Error(),
		})
		return
	}
	userId, err := getUserId(string(authToken))
	if err != nil {
		ctx.JSON(500, structs.Error{
			//failed fetching user id
			Reason: "ffu",
			Msg:    err.Error(),
		})
		return
	}
	if userId != registration.SupposedUserId {
		ctx.JSON(400, structs.Error{
			//invalid user id
			Reason: "iui",
			Msg:    fmt.Sprintf("%s != %s", userId, registration.SupposedUserId),
		})
		return
	}
	stmt, err := db.PrepareContext(ctx, "INSERT INTO devices (user_id,fcm_token,is_firebase,failed_attempts) VALUES ($1,$2,$3,0)")
	if err != nil {
		ctx.JSON(500, structs.Error{
			//databse prepare fail
			Reason: "dpf",
			Msg:    err.Error(),
		})
		return
	}
	_, err = stmt.ExecContext(ctx, userId, registration.FcmToken, registration.IsFirebase)
	if err != nil {
		ctx.JSON(500, structs.Error{
			//database insert fail
			Reason: "dif",
			Msg:    err.Error(),
		})
		return
	}
	for i := range authToken {
		authToken[i] = 0
	}
	ctx.Status(201)
}

func getUserId(token string) (int, error) {
	client := http.Client{}
	req, err := http.NewRequest("GET", "https://traewelling.de/api/v1/auth/user", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("User-Agent", "Traewelcross-push/1.0.1 (https://github.com/traewelcross/traewelcross_push; traewelcross.de)")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	defer res.Body.Close()
	if err != nil {
		return 0, err
	}
	data := make(map[string](map[string]interface{}))
	rawData, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println("!ERROR!")
		return 0, err
	}
	err = json.Unmarshal([]byte(rawData), &data)
	if err != nil {
		fmt.Println("!JSON ERROR!")
		return 0, err
	}
	fmt.Println(data)
	fmt.Println(int(data["data"]["id"].(float64)))
	if _, ok := data["data"]["id"].(float64); !ok {
		return 0, errors.New("couldn't find id")
	}
	return int(data["data"]["id"].(float64)), nil
}

func createDevicesTable(db *sql.DB) error {
	createDeviceTableSQL := `
  CREATE TABLE IF NOT EXISTS devices (
          id SERIAL PRIMARY KEY,
          user_id INT NOT NULL,
          fcm_token TEXT NOT NULL UNIQUE,
		  failed_attempts INT NOT NULL,
		  is_firebase boolean NOT NULL,
          created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
          updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
  )`
	_, err := db.ExecContext(context.Background(), createDeviceTableSQL)
	return err
}

func createEventsTable(db *sql.DB) error {
	createEventsTableSQL := `
	CREATE TABLE IF NOT EXISTS processed_events(
	event_uuid TEXT PRIMARY KEY)`
	_, err := db.ExecContext(context.Background(), createEventsTableSQL)
	return err
}

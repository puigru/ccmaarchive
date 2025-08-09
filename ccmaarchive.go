package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"

	"ccmaarchive/auth"
	"ccmaarchive/routes"
)

func main() {
	pool, err := pgxpool.New(context.Background(), "postgres://postgres:changeit@postgres:5432/postgres")
	if err != nil {
		log.Fatalf("database init failed: %s\n", err)
	}

	_, err = pool.Exec(context.Background(), `
	-- CREATE TABLES
	CREATE TABLE IF NOT EXISTS client (
		id INTEGER PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
		oauth_id TEXT NOT NULL,
		oauth_secret TEXT NOT NULL,
		created TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT (now() AT TIME ZONE 'utc')
	);
	CREATE TABLE IF NOT EXISTS video (
		id INTEGER,
		version INTEGER,
		data JSON NOT NULL,
		data_diff TEXT,
		client_id INTEGER NOT NULL,
		created TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT (now() AT TIME ZONE 'utc'),
		PRIMARY KEY(id, version),
		CONSTRAINT fk_client FOREIGN KEY(client_id) REFERENCES client(id)
	);
	CREATE TABLE IF NOT EXISTS video_version_tracker (
		video_id INTEGER PRIMARY KEY,
		current_version INTEGER NOT NULL DEFAULT 0
	);
	-- CREATE INDEXES
	CREATE INDEX IF NOT EXISTS video_id ON video(id);
	CREATE INDEX IF NOT EXISTS video_progid ON video(((data->'informacio'->>'programa_id')::INT));
	-- CREATE VIEWS
	CREATE OR REPLACE VIEW video_current AS
	SELECT v.*
	FROM video_version_tracker vt
	JOIN video v ON vt.video_id = v.id AND vt.current_version = v.version;
	-- CREATE TRIGGERS
	CREATE EXTENSION IF NOT EXISTS plsh;
	CREATE OR REPLACE FUNCTION video_calculate_diff(new_data TEXT, old_data TEXT)
	RETURNS TEXT
	LANGUAGE 'plsh'
	AS $$
#!/bin/bash
diff -u --label=original --label=modified <(echo "$2" | jq) <(echo "$1" | jq) || exit 0
	$$;
	CREATE OR REPLACE FUNCTION video_calculate_version()
		RETURNS TRIGGER
		LANGUAGE 'plpgsql'
	AS $$
	DECLARE
		previous_version INT;
		previous_data JSON;
		sanitized_new_data JSONB;
		sanitized_previous_data JSONB;
	BEGIN
		-- Ensure there is a row in video_version_tracker for this video
		INSERT INTO video_version_tracker(video_id, current_version)
		VALUES (NEW.id, 0)
		ON CONFLICT (video_id) DO NOTHING;

		-- Lock the row in video_version_tracker for this video
		SELECT vt.current_version INTO previous_version
		FROM video_version_tracker vt
		WHERE vt.video_id = NEW.id
		FOR UPDATE;

		-- Now retrieve previous data if it exists
		SELECT v.data INTO previous_data
		FROM video v
		WHERE v.id = NEW.id AND v.version = previous_version;

		-- Exclude fields from JSON that are irrelevant for versioning
		sanitized_new_data := NEW.data::JSONB
			- 'audiencies' - 'publicitat' - 'youbora';
		sanitized_previous_data := previous_data::JSONB
			- 'audiencies' - 'publicitat' - 'youbora';

		-- Check if there is no previous version (initial insert case)
		IF previous_data IS NULL THEN
			NEW.version := 0;
			NEW.data_diff := NULL;
		-- Handle case where a previous version exists
		ELSE
			-- Compare sanitized data
			IF (sanitized_previous_data IS NOT DISTINCT FROM sanitized_new_data) THEN
				RAISE EXCEPTION 'No change from last version';
			END IF;
			SELECT video_calculate_diff(NEW.data::TEXT, previous_data::TEXT) INTO NEW.data_diff;
			NEW.version := previous_version + 1;
		END IF;

		-- Update video_version_tracker
		UPDATE video_version_tracker
		SET current_version = NEW.version
		WHERE video_id = NEW.id;

		RETURN NEW;
	END
	$$;
	CREATE OR REPLACE TRIGGER video_calculate_version BEFORE
		INSERT ON video FOR EACH ROW EXECUTE PROCEDURE video_calculate_version();
	`)
	if err != nil {
		log.Fatalf("failed to execute setup SQL: %s\n", err)
	}

	var action string
	flag.StringVar(&action, "action", "", "Perform server management action")
	flag.Parse()
	if action != "" {
		switch strings.ToLower(action) {
		case "register-client":
			clientId, clientSecret, err := auth.RegisterClient(pool)
			if err != nil {
				fmt.Printf("error: %s\n", err)
				return
			}
			fmt.Printf("client ID: %s\n", clientId)
			fmt.Printf("client secret: %s\n", clientSecret)
		default:
			log.Fatalf("Unknown action: %s\n", action)
		}
		return
	}

	router := httprouter.New()
	router.PUT("/private/video/:id", auth.OAuthMiddleware(pool, routes.PutVideo(pool)))
	router.POST("/private/oauth/token", auth.OAuthTokenEndpoint(pool))
	router.GET("/pvideo/media.jsp", routes.MediaJsp(pool))
	router.GET("/videos", routes.Videos(pool))

	log.Fatal(http.ListenAndServe(":8080", router))
}

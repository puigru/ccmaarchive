package routes

import (
	"ccmaarchive/auth"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"github.com/tidwall/gjson"
)

func PutVideo(pool *pgxpool.Pool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		data, _ := io.ReadAll(r.Body)
		id := ps.ByName("id")
		if _, err := strconv.Atoi(id); err != nil {
			http.Error(w, "Bad video id", http.StatusBadRequest)
			return
		}
		if !gjson.Valid(string(data)) {
			http.Error(w, "Bad data", http.StatusBadRequest)
			return
		}
		if !gjson.Get(string(data), "informacio.estat.actiu").Bool() {
			http.Error(w, "Unpublished video", http.StatusBadRequest)
			return
		}
		authContext := auth.RequireContext(r.Context())
		_, err := pool.Exec(context.Background(), `INSERT INTO video (id, data, client_id) VALUES ($1, $2, $3)`, id, data, authContext.ClientId)
		if err != nil {
			var pgErr *pgconn.PgError
			// Check if the error was raised by a database function
			if errors.As(err, &pgErr) && pgErr.Code == "P0001" {
				http.Error(w, pgErr.Message, http.StatusBadRequest)
				return
			}
			log.Printf("database error: %s\n", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
	}
}

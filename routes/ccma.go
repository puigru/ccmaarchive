package routes

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
)

const ERROR_GENERIC string = "S'ha produït un error"

func sendError(w http.ResponseWriter, code int, text string) {
	w.Header().Add("content-type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"code":%d,"text":"%s"}`+"\n", code, text)
}

func MediaJsp(pool *pgxpool.Pool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		id := r.URL.Query().Get("idint")
		if id == "" {
			sendError(w, 400, ERROR_GENERIC)
			return
		}
		var response string
		err := pool.QueryRow(context.Background(),
			"SELECT data FROM video_current WHERE id = $1", id).Scan(&response)
		if err != nil {
			sendError(w, 404, ERROR_GENERIC)
			return
		}
		w.Header().Add("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintln(w, response)
	}
}

func Videos(pool *pgxpool.Pool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		params := make(pgx.NamedArgs)
		for k, values := range r.URL.Query() {
			params[k] = values[0]
		}
		var conds []string
		if _, ok := params["id"]; ok {
			conds = append(conds, `id = @id`)
		}
		if _, ok := params["programatv_id"]; ok {
			conds = append(conds, `((data->'informacio'->>'programa_id')::INT) = @programatv_id`)
		}
		if _, ok := params["temporada"]; ok {
			conds = append(conds, `data->'informacio'->'temporada'->>'idName' = @temporada`)
		}
		if _, ok := params["tipus_contingut"]; ok {
			conds = append(conds, `data->'informacio'->>'tipus_contingut' = @tipus_contingut`)
		}
		var orderBy string
		switch params["ordre"] {
		case nil:
			fallthrough
		case "id":
			orderBy = "id"
		case "capitol":
			orderBy = "((data->'informacio'->>'capitol')::INT)"
		default:
			sendError(w, 500, "Server error: 500")
			return
		}
		perPage := 20
		if perPageStr, ok := params["items_pagina"]; ok {
			var err error
			if perPage, err = strconv.Atoi(perPageStr.(string)); err != nil {
				// TODO: check error message
				sendError(w, 400, "")
				return
			}
		}
		page := 1
		if pageStr, ok := params["pagina"]; ok {
			var err error
			if page, err = strconv.Atoi(pageStr.(string)); err != nil {
				// TODO: check error message
				sendError(w, 400, "")
				return
			}
		}
		where := strings.Join(conds, " AND ")
		var totalRows int
		pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM video_current WHERE `+where, params).Scan(&totalRows)
		query := fmt.Sprintf(`SELECT json_build_object(
			'embedable', false,
			'entradeta', data->'informacio'->>'descripcio',
			'avantitol', data->'informacio'->>'programa',
			'produccio', (data->'informacio'->>'op')::INT,
			'tipus_contingut', data->'informacio'->>'tipus_contingut',
			'durada', data->'informacio'->'durada'->>'text' || ':00',
			'tipologia', 'DTY_VIDEO_MM',
			'capitol', COALESCE((data->'informacio'->>'capitol')::INT, -1),
			'temporades', json_build_array(
				json_build_object(
					'id', data->'informacio'->'temporada'->>'idName',
					'name', data->'informacio'->'temporada'->>'name',
					'main', 'true'
				) 
			),
			'nom_friendly', data->'informacio'->>'slug',
			'permatitle', data->'informacio'->>'titol',
			'programa', data->'informacio'->>'programa',
			'versio', 1,
			'id', (data->'informacio'->>'id')::INT,
			'data_modificacio', data->'informacio'->'data_emissio'->>'text' || ':00',
			'titol', data->'informacio'->>'titol',
			'data_publicacio', data->'informacio'->'data_emissio'->>'text' || ':00',
			'domini', 'SENSE',
			'data_emissio', data->'informacio'->'data_emissio'->>'text' || ':00'
		) FROM video_current WHERE %s ORDER BY %s ASC LIMIT %d OFFSET %d`, where, orderBy, perPage, perPage*(page-1))
		fmt.Printf("Executing query=%s, params=%v\n", query, params)
		rows, err := pool.Query(context.Background(), query, params)
		if err != nil {
			sendError(w, 404, ERROR_GENERIC)
			fmt.Printf("Failed query:%s\n", err)
			return
		}
		items := []string{}
		for rows.Next() {
			var item string
			rows.Scan(&item)
			items = append(items, item)
		}
		re := regexp.MustCompile(`\s`)
		response := fmt.Sprintf(re.ReplaceAllString(`{
			"resposta": {
				"status": "OK",
				"items": {
					"num": %[2]d,
					"item": [
						%[1]s
					]
				},
				"paginacio": {
					"total_items": %[3]d,
					"items_pagina": %[4]d,
					"pagina_actual": %[5]d,
					"total_pagines": %.0[6]f
				}
			}
		}`, ""), strings.Join(items, ","), len(items), totalRows, perPage, page, math.Ceil(float64(totalRows)/float64(perPage)))
		w.Header().Add("Content-Type", "application/json; charset=utf-8")
		fmt.Fprintln(w, response)
	}
}

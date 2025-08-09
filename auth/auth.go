package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/julienschmidt/httprouter"
	"gopkg.in/go-jose/go-jose.v2"
)

const OAUTH_ID_LENGTH int = 16
const OAUTH_SECRET_LENGTH int = 32
const ACCESS_TOKEN_DURATION int = 3600

type OAuthToken struct {
	OAuthId   string `json:"clientId"`
	ExpiresAt int64  `json:"expiresAt"`
}

type AuthContext struct {
	ClientId int
}

type key int

var contextKey key

func createToken(oauthId string, oauthSecret string, expiresIn int) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(oauthSecret)}, nil)
	if err != nil {
		return "", err
	}
	expires_at := time.Now().Unix() + int64(expiresIn)
	token := OAuthToken{OAuthId: oauthId, ExpiresAt: expires_at}
	jsonToken, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(jsonToken)
	if err != nil {
		return "", err
	}
	jwsCompact, err := jws.CompactSerialize()
	if err != nil {
		return "", err
	}
	return jwsCompact, nil
}

func validateToken(pool *pgxpool.Pool, tokenString string) (*AuthContext, error) {
	jws, err := jose.ParseSigned(tokenString)
	if err != nil {
		return nil, err
	}
	var token OAuthToken
	if err := json.Unmarshal(jws.UnsafePayloadWithoutVerification(), &token); err != nil {
		return nil, err
	}
	row := pool.QueryRow(context.Background(),
		"SELECT id, oauth_secret FROM client WHERE oauth_id = $1", token.OAuthId)
	var clientId int
	var oauthSecret string
	if err = row.Scan(&clientId, &oauthSecret); err != nil {
		return nil, err
	}
	if _, err = jws.Verify([]byte(oauthSecret)); err != nil {
		return nil, err
	}
	if token.ExpiresAt < time.Now().Unix() {
		return nil, nil
	}
	return &AuthContext{ClientId: clientId}, nil
}

func RegisterClient(pool *pgxpool.Pool) (string, string, error) {
	bytes := make([]byte, OAUTH_ID_LENGTH+OAUTH_SECRET_LENGTH)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	oauthId := hex.EncodeToString(bytes[:OAUTH_ID_LENGTH])
	oauthSecret := hex.EncodeToString(bytes[OAUTH_ID_LENGTH:])
	_, err := pool.Exec(context.Background(), `INSERT INTO client (oauth_id, oauth_secret) VALUES ($1, $2)`, oauthId, oauthSecret)
	if err != nil {
		return "", "", err
	}
	return oauthId, oauthSecret, nil
}

func RequireContext(ctx context.Context) *AuthContext {
	authContext, ok := ctx.Value(contextKey).(*AuthContext)
	if !ok {
		panic("authContext")
	}
	return authContext
}

func sendOAuthError(w http.ResponseWriter, err string, code int) {
	w.Header().Add("content-type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":"%s"}`+"\n", err)
}

// This only aims to support the client_credentials grant (see RFC 6749 Section 4.4)
// https://datatracker.ietf.org/doc/html/rfc6749#section-4.4
func OAuthTokenEndpoint(pool *pgxpool.Pool) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if err := r.ParseForm(); err != nil {
			sendOAuthError(w, "invalid_request", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" {
			sendOAuthError(w, "unsupported_grant_type", http.StatusBadRequest)
			return
		}
		client_id, client_secret, ok := r.BasicAuth()
		if !ok {
			w.Header().Add("WWW-Authenticate", "Basic")
			sendOAuthError(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		rows, err := pool.Query(context.Background(),
			"SELECT 1 FROM client WHERE oauth_id = $1 AND oauth_secret = $2", client_id, client_secret)
		if err != nil {
			fmt.Println(err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		if !rows.Next() {
			w.Header().Add("WWW-Authenticate", "Basic")
			sendOAuthError(w, "invalid_client", http.StatusUnauthorized)
			return
		}
		token, err := createToken(client_id, client_secret, ACCESS_TOKEN_DURATION)
		if err != nil {
			fmt.Println(err)
			http.Error(w, "Token generation error", http.StatusInternalServerError)
			return
		}
		// https://datatracker.ietf.org/doc/html/rfc6749#section-5.1
		re := regexp.MustCompile(`\s`)
		response := fmt.Sprintf(re.ReplaceAllString(`{
			"access_token": "%s",
			"token_type": "Bearer",
			"expires_in": %d
		}`, ""), token, ACCESS_TOKEN_DURATION)
		w.Header().Add("Content-Type", "application/json; charset=utf-8")
		w.Header().Add("Cache-Control", "no-store")
		fmt.Fprintln(w, response)
	}
}

// https://datatracker.ietf.org/doc/html/rfc6750#section-3.1
func OAuthMiddleware(pool *pgxpool.Pool, next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header().Add("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authHeaderParts := strings.Split(authHeader, " ")
		if len(authHeaderParts) != 2 || strings.ToLower(authHeaderParts[0]) != "bearer" {
			w.Header().Add("WWW-Authenticate", "Bearer")
			sendOAuthError(w, "invalid_token", http.StatusUnauthorized)
			return
		}
		authContext, _ := validateToken(pool, authHeaderParts[1])
		if authContext == nil {
			w.Header().Add("WWW-Authenticate", "Bearer")
			sendOAuthError(w, "invalid_token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), contextKey, authContext)
		r = r.WithContext(ctx)
		next(w, r, ps)
	}
}

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/3mrmousa/chirpy/internal/auth"
	"github.com/3mrmousa/chirpy/internal/database"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)


const filePathRoot = "."
const port = "8080"

func main() {
	godotenv.Load()
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	secret := os.Getenv("SECRET")
	polkaKey := os.Getenv("POLKA_KEY")

	db, err := sql.Open("postgres", dbURL)

	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	dbQueries := database.New(db)

	mux := http.NewServeMux()

	cfg := apiConfig{
		dbQueries: dbQueries,
		platform: platform,
		secret: secret,
		polkaKey: polkaKey,
	}

	fileServer := http.FileServer(http.Dir(filePathRoot))
	mux.Handle("GET /app/", cfg.middlewareMeticsInc(http.StripPrefix("/app", fileServer)))
	mux.HandleFunc("GET /admin/metrics", cfg.metricsHandler)
	mux.HandleFunc("GET /api/chirps", cfg.getAllChirpsHandler)
	mux.HandleFunc("GET /api/chirps/{id}", cfg.getChirpByIdHandler)
	mux.HandleFunc("GET /api/healthz", healthzHandler)
	mux.HandleFunc("POST /admin/reset", cfg.resetHandler)
	mux.HandleFunc("POST /api/chirps", cfg.createChirpHandler)
	mux.HandleFunc("DELETE /api/chirps/{id}", cfg.deleteChirpHandler)
	mux.HandleFunc("POST /api/login", cfg.loginHandler)
	mux.HandleFunc("POST /api/refresh", cfg.refreshTokenHandler)
	mux.HandleFunc("POST /api/revoke", cfg.revokeTokenHandler)
	mux.HandleFunc("POST /api/users", cfg.createUserHandler)
	mux.HandleFunc("PUT /api/users", cfg.updateUserHandler)
	mux.HandleFunc("POST /api/polka/webhooks", cfg.polkaWebhookHandler)

	server := &http.Server{
		Handler: mux,
		Addr: ":" + port,
	}

	log.Printf("Serving files from %s on port: %s\n", filePathRoot, port)
	log.Fatal(server.ListenAndServe())
}

// Handlers

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (cfg *apiConfig) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	html := fmt.Sprintf(`<html>

<body>
    <h1>Welcome, Chirpy Admin</h1>
    <p>Chirpy has been visited %d times!</p>
</body>

</html>`, cfg.fileserverHits.Load())
	w.Write([]byte(html))
}

func (cfg *apiConfig) resetHandler(w http.ResponseWriter, r *http.Request) {

	if cfg.platform != "dev" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	cfg.fileserverHits.Store(0)
	cfg.dbQueries.DeleteAllUsers(r.Context())

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Hits reset to 0"))
}

func (cfg *apiConfig) createChirpHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Body string `json:"body"`
	}

	decoder := json.NewDecoder(r.Body)
	data := parameters{}

	err := decoder.Decode(&data)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	tokenString, err := auth.GetBearerToken(r.Header)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	userID, err := auth.ValidateJWT(tokenString, cfg.secret)
	if err != nil {
		http.Error(w, "Invalid token", http.StatusUnauthorized)
		return	
	}

	if len(data.Body) > 140 {
		http.Error(w, "Chirp must be at least 140 characters long", http.StatusBadRequest)
		return	
	}
	
	badWords := []string{"kerfuffle", "fornax",
	 "sharbert", "profane",
	 "Kerfuffle", "Fornax",
	  "Sharbert", "Profane"}

	message := make([]string, 0)

	for word := range strings.FieldsSeq(data.Body) {
		isBadWord := slices.Contains(badWords, word)

		if isBadWord {
			message = append(message, "****")
		} else {
			message = append(message, word)
		}
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body string `json:"body"`
		UserID uuid.UUID `json:"user_id"`
	}

	createdChirp, err := cfg.dbQueries.CreateChirp(r.Context(), database.CreateChirpParams{
		Body:   strings.Join(message, " "),
		UserID: userID,
	})
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	res := response{
		ID: createdChirp.ID,
		CreatedAt: createdChirp.CreatedAt,
		UpdatedAt: createdChirp.UpdatedAt,
		Body: createdChirp.Body,
		UserID: createdChirp.UserID,
	}

	resData, err := json.Marshal(res)

	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(resData)
}

func (cfg *apiConfig) getAllChirpsHandler(w http.ResponseWriter, r *http.Request) {
	authorIDString := r.URL.Query().Get("author_id")

	var chirps []database.Chirp
	var err error

	if authorIDString != "" {
		authorID, parseErr := uuid.Parse(authorIDString)
		if parseErr != nil {
			http.Error(w, "Invalid author ID", http.StatusBadRequest)
			return
		}
		chirps, err = cfg.dbQueries.GetChirpsByAuthor(r.Context(), authorID)
	} else {
		chirps, err = cfg.dbQueries.GetAllChirps(r.Context())
	}

	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return	
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body string `json:"body"`
		UserID uuid.UUID `json:"user_id"`
	}

	res := make([]response, 0)

	for _, chirp := range chirps {
		res = append(res, response{
			ID: chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body: chirp.Body,
			UserID: chirp.UserID,
		})
	}

	resData, err := json.Marshal(res)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resData)
}

func (cfg *apiConfig) getChirpByIdHandler(w http.ResponseWriter, r *http.Request) {
	chirpID := r.PathValue("id")
	parsedID, err := uuid.Parse(chirpID)
	if err != nil {
		http.Error(w, "Invalid chirp ID", http.StatusUnprocessableEntity)
		return	
	}

	chirp, err := cfg.dbQueries.GetChirpById(r.Context(), parsedID)
	if err != nil {
		http.Error(w, "Chirp not found", http.StatusNotFound)
		return	
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body string `json:"body"`
		UserID uuid.UUID `json:"user_id"`
	}

	res := response{
		ID: chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		Body: chirp.Body,
		UserID: chirp.UserID,
	}

	resData, err := json.Marshal(res)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resData)	
}

func (cfg *apiConfig) loginHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Email string `json:"email"`
		Password string `json:"password"`
	}

	decoder := json.NewDecoder(r.Body)
	data := parameters{}

	err := decoder.Decode(&data)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}



	user, err := cfg.dbQueries.GetUserByEmail(r.Context(), data.Email)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return	
	}

	match, err := auth.CheckPasswordHash(data.Password, user.HashedPassword)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return	
	}
	if !match {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return	
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Email string `json:"email"`
		Token string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		IsChirpyRed bool `json:"is_chirpy_red"`
	}

	token, err := auth.MakeJWT(user.ID, cfg.secret, time.Duration(3600)*time.Second)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}
	refreshToken, err := auth.MakeRefreshToken()
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	_, err = cfg.dbQueries.CreateRefreshToken(r.Context(), database.CreateRefreshTokenParams{
		Token:     refreshToken,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(time.Hour * 24 * 60),
	})
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	res := response{
		ID: user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
		Email: user.Email,
		Token: token,
		RefreshToken: refreshToken,
		IsChirpyRed: user.IsChirpyRed,
	}

	resData, err := json.Marshal(res)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resData)

}

func (cfg *apiConfig) refreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	authToken := r.Header.Get("Authorization")
	if authToken == "" {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}
	
	tokenString := strings.TrimPrefix(authToken, "Bearer ")
	if tokenString == authToken || tokenString == "" {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	user, err := cfg.dbQueries.GetUserFromRefreshToken(r.Context(), tokenString)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	accessToken, err := auth.MakeJWT(user.ID, cfg.secret, time.Hour)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		Token string `json:"token"`
	}

	resData, err := json.Marshal(response{
		Token: accessToken,
	})
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resData)
}

func (cfg *apiConfig) revokeTokenHandler(w http.ResponseWriter, r *http.Request) {
	authToken := r.Header.Get("Authorization")
	if authToken == "" {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}
	
	tokenString := strings.TrimPrefix(authToken, "Bearer ")
	if tokenString == authToken || tokenString == "" {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	err := cfg.dbQueries.RevokeRefreshToken(r.Context(), tokenString)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) createUserHandler(w http.ResponseWriter, r *http.Request) {
	type User struct {
		Password string `json:"password"`
		Email string `json:"email"`
	}

	decoder := json.NewDecoder(r.Body)
	user := User{}

	err := decoder.Decode(&user)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		Created_at time.Time `json:"created_at"`
		Updated_at time.Time `json:"updated_at"`
		Email string `json:"email"`
		IsChirpyRed bool `json:"is_chirpy_red"`
	}

	hashedPassword, err := auth.HashPassword(user.Password)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	createdUser, err := cfg.dbQueries.CreateUser(r.Context(), database.CreateUserParams{
		Email: user.Email,
		HashedPassword: hashedPassword,
	})
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	res := response{
		ID: createdUser.ID,
		Created_at: createdUser.CreatedAt,
		Updated_at: createdUser.UpdatedAt,
		Email: createdUser.Email,
		IsChirpyRed: createdUser.IsChirpyRed,
	}

	data, err := json.Marshal(res)

	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(data)
}

func (cfg *apiConfig) updateUserHandler(w http.ResponseWriter, r *http.Request) {
	type Data struct {
		Password string `json:"password"`
		Email string `json:"email"`
	}

	authToken := r.Header.Get("Authorization")
	if authToken == "" {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authToken, "Bearer ")
	if token == authToken || token == "" {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}
	
	decoder := json.NewDecoder(r.Body)
	data := Data{}

	err = decoder.Decode(&data)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	hashedPassword, err := auth.HashPassword(data.Password)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	updatedUser, err := cfg.dbQueries.UpdateUserByEmail(r.Context(), database.UpdateUserByEmailParams{
		ID: userID,
		Email: data.Email,
		HashedPassword: hashedPassword,
	})

	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Email string `json:"email"`
		IsChirpyRed bool `json:"is_chirpy_red"`
	}

	res := response{
		ID: updatedUser.ID,
		CreatedAt: updatedUser.CreatedAt,
		UpdatedAt: updatedUser.UpdatedAt,
		Email: updatedUser.Email,
		IsChirpyRed: updatedUser.IsChirpyRed,
	}

	resData, err := json.Marshal(res)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(resData)
}

func (cfg *apiConfig) deleteChirpHandler(w http.ResponseWriter, r *http.Request) {
	chirpIdStr := r.PathValue("id")
	if chirpIdStr == "" {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	chirpID, err := uuid.Parse(chirpIdStr)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}
	
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader || token == "" {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.secret)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	chirp, err := cfg.dbQueries.GetChirpById(r.Context(), chirpID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusNotFound)
		return
	}

	if chirp.UserID != userID {
		http.Error(w, "Something went wrong", http.StatusForbidden)
		return
	}

	err = cfg.dbQueries.DeleteChirpById(r.Context(), chirpID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) polkaWebhookHandler(w http.ResponseWriter, r *http.Request) {
	type Request struct {
		Event string `json:"event"`
		Data struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	
	decoder := json.NewDecoder(r.Body)
	req := Request{}

	err := decoder.Decode(&req)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	apiKey, err := auth.GetAPIKey(r.Header)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	if apiKey != cfg.polkaKey {
		http.Error(w, "Something went wrong", http.StatusUnauthorized)
		return
	}

	if req.Event != "user.upgraded" {
		w.WriteHeader(http.StatusNoContent)
    	return
	}

	userID, err := uuid.Parse(req.Data.UserID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusBadRequest)
		return	
	}

	_, err = cfg.dbQueries.GetUserByID(r.Context(), userID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusNotFound)
		return	
	}

	_, err = cfg.dbQueries.UpgradeUserToChirpyRed(r.Context(), userID)
	if err != nil {
		http.Error(w, "Something went wrong", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Middlewares

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries *database.Queries
	platform string
	secret string
	polkaKey string
}

func (cfg *apiConfig) middlewareMeticsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}
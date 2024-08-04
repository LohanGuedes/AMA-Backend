package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/lohanguedes/AMA-Backend/internal/store/pgstore"
)

type apiHandler struct {
	queries     *pgstore.Queries
	router      *chi.Mux
	subscribers map[string]map[*websocket.Conn]context.CancelFunc
	upgrader    websocket.Upgrader
	mu          *sync.Mutex
}

func NewHandler(q *pgstore.Queries) http.Handler {
	api := apiHandler{
		queries: q,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		subscribers: make(map[string]map[*websocket.Conn]context.CancelFunc),
		mu:          &sync.Mutex{},
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, middleware.Logger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/subscribe/{room_id}", api.handleSubscribe)

	r.Route("/api", func(r chi.Router) {
		r.Route("/rooms", func(r chi.Router) {
			r.Post("/", api.handleCreateRoom)
			r.Get("/", api.handleGetRooms)

			r.Route("/{room_id}/messages", func(r chi.Router) {
				r.Get("/", api.handleGetRoomMessages)
				r.Post("/", api.handleCreateRoomMessage)

				r.Route("/{message_id}", func(r chi.Router) {
					r.Get("/", api.handleGetRoomMessage)
					r.Patch("/react", api.handleReactToMessage)
					r.Delete("/react", api.handleRemoveReactionFromMessage)
					r.Patch("/answer", api.handleMarkMessageAsAnswered)
				})
			})
		})
	})

	api.router = r
	return api
}

func (api apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api.router.ServeHTTP(w, r)
}

const (
	MessageKindMessageCreated = "message_created"
)

type MessageMessageCreated struct {
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
}

type Message struct {
	Kind   string `json:"kind"`
	Value  any    `json:"value"`
	RoomID string `json:"-"`
}

func (api apiHandler) notifyClients(msg Message) {
	api.mu.Lock()
	defer api.mu.Unlock()

	subscribers, ok := api.subscribers[msg.RoomID]
	if !ok || len(subscribers) == 0 {
		slog.Warn("No subscribers on room id")
		return
	}

	for conn, cancel := range subscribers {
		if err := conn.WriteJSON(msg); err != nil {
			slog.Error("failed to send message to client", "error", err)
			cancel()
		}
	}
}

// Websocket
func (api apiHandler) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")

	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	_, err = api.queries.GetRoom(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "invalid room id", http.StatusNotFound)
			return
		}
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	conn, err := api.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("failed to upgrade conn", "error", err)
		http.Error(w, "could not upgrade connection to websocket", http.StatusBadRequest)
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())

	api.mu.Lock()
	if _, ok := api.subscribers[rawRoomID]; !ok {
		api.subscribers[rawRoomID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	slog.Info("new client connected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
	api.subscribers[rawRoomID][conn] = cancel
	api.mu.Unlock()
	<-ctx.Done()

	api.mu.Lock()
	slog.Info("new client disconnected", "room_id", rawRoomID, "client_ip", r.RemoteAddr)
	delete(api.subscribers[rawRoomID], conn)
	api.mu.Unlock()
}

func (api apiHandler) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	type _body struct {
		Theme string `json:"theme"`
	}
	var body _body

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusUnprocessableEntity)
		return
	}

	roomId, err := api.queries.InsertRoom(r.Context(), body.Theme)
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	type response struct {
		ID string `json:"id"`
	}

	data, err := json.Marshal(map[string]any{
		"id": roomId.String(),
	})
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	w.Write(data)
	w.Header().Set("Content-Type", "application/json")
}

func (api apiHandler) handleGetRooms(w http.ResponseWriter, r *http.Request) {
}

func (api apiHandler) handleGetRoomMessages(w http.ResponseWriter, r *http.Request) {
	panic("implement")
}

func (api apiHandler) handleCreateRoomMessage(w http.ResponseWriter, r *http.Request) {
	rawRoomID := chi.URLParam(r, "room_id")

	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	_, err = api.queries.GetRoom(ctx, roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "invalid room id", http.StatusNotFound)
			return
		}
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	body := struct {
		Message string `json:"message"`
	}{}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	messageID, err := api.queries.InsertMessage(r.Context(), pgstore.InsertMessageParams{
		RoomID:  roomID,
		Message: body.Message,
	})
	if err != nil {
		slog.Error("failed to insert message", "error", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(map[string]any{
		"id": messageID.String(),
	})
	if err != nil {
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)

	go api.notifyClients(Message{
		Kind:   MessageKindMessageCreated,
		RoomID: rawRoomID,
		Value: MessageMessageCreated{
			ID:      messageID.String(),
			Message: body.Message,
		},
	})
}

func (api apiHandler) handleGetRoomMessage(w http.ResponseWriter, r *http.Request) {
	panic("implement")
}

func (api apiHandler) handleReactToMessage(w http.ResponseWriter, r *http.Request) {
	panic("implement")
}

func (api apiHandler) handleRemoveReactionFromMessage(w http.ResponseWriter, r *http.Request) {
	panic("implement")
}

func (api apiHandler) handleMarkMessageAsAnswered(w http.ResponseWriter, r *http.Request) {
	panic("implement")
}

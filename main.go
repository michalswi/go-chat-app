package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},

	// Restrict Origins for security (avoid '# CORS')
	// CheckOrigin: func(r *http.Request) bool {
	// 	allowedOrigins := []string{
	// 		"https://yourdomain.com",
	// 		"https://www.yourdomain.com",
	// 	}
	// 	origin := r.Header.Get("Origin")
	// 	for _, allowed := range allowedOrigins {
	// 		if origin == allowed {
	// 			return true
	// 		}
	// 	}
	// 	return false
	// },
}

var connections = make(map[string]*websocket.Conn)
var mu sync.Mutex
var logger = log.New(os.Stdout, "chat-app-server ", log.LstdFlags|log.Lshortfile|log.Ltime|log.LUTC)

var redisClient *redis.Client
var ctx = context.Background()

// rate limiting
const maxConnectionsPerIP = 1
const maxConnectionsPerUser = 1

var ipConnections = make(map[string]int)

func main() {
	port := getEnv("SERVER_PORT", "443")
	certFile := getEnv("CERT", "./certs/cert.crt")
	keyFile := getEnv("KEY", "./certs/cert.key")
	redisAddr := getEnv("REDIS_ADDR", "")
	redisPass := getEnv("REDIS_PASS", "")

	srv := &http.Server{
		Addr:         ":" + port,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if redisAddr != "" {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: redisPass,
			DB:       0,
			// TLSConfig: &tls.Config{
			// 	InsecureSkipVerify: true,
			// },
		})
		_, err := redisClient.Ping(ctx).Result()
		if err != nil {
			logger.Fatalf("Failed to connect to Redis: %v", err)
		}
		logger.Println("Connected to Redis")
	}

	http.HandleFunc("/hz", hz)
	http.HandleFunc("/ws", rateLimit(handleConnections))

	go func() {
		logger.Println("Server is ready to handle requests at port", port)
		if err := http.ListenAndServeTLS(":"+port, certFile, keyFile, nil); !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("Failed to start TLS server: %v", err)
		}
		logger.Println("Stopped serving new connections.")
	}()

	// shutdown server
	gracefulShutdown(srv)
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	_, msg, err := conn.ReadMessage()
	if err != nil {
		logger.Println("Error reading username:", err)
		return
	}
	username := string(msg)
	clientIP := r.RemoteAddr

	mu.Lock()
	if _, exists := connections[username]; exists {
		conn.WriteMessage(websocket.TextMessage, []byte("Username already in use. Please choose another."))
		mu.Unlock()
		return
	}
	if countConnectionsByUsername(username) >= maxConnectionsPerUser {
		conn.WriteMessage(websocket.TextMessage, []byte("Too many connections for this username"))
		logger.Printf("Too many connections from this username: %s.", username)
		mu.Unlock()
		return
	}
	connections[username] = conn
	mu.Unlock()

	if redisClient != nil {
		err := redisClient.Set(ctx, username, clientIP, 0).Err()
		if err != nil {
			logger.Println("Error storing username in Redis:", err)
		}
	}

	logger.Printf("User '%s' connected from IP %s.", username, clientIP)
	conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Welcome %s! You are now connected.", username)))

	defer func() {
		mu.Lock()
		delete(connections, username)
		mu.Unlock()
		if redisClient != nil {
			err := redisClient.Del(ctx, username).Err()
			if err != nil {
				logger.Println("Error deleting username from Redis:", err)
			}
		}
		logger.Printf("User '%s' disconnected.", username)
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			logger.Printf("Connection error with user '%s': %v", username, err)
			break
		}
		messageParts := string(msg)

		// enforce a maximum message length
		if len(messageParts) > 1024 {
			conn.WriteMessage(websocket.TextMessage, []byte("Message too long."))
			continue
		}

		separator := "|"
		idx := strings.Index(messageParts, separator)
		if idx != -1 {
			target := messageParts[:idx]
			message := messageParts[idx+1:]
			sendPrivateMessage(username, target, message)
		} else {
			broadcastMessage(fmt.Sprintf("[%s]: %s", username, messageParts), username)
		}
	}
}

func sendPrivateMessage(sender, recipient, message string) {
	mu.Lock()
	defer mu.Unlock()

	recipientConn, exists := connections[recipient]
	senderConn := connections[sender]

	if !exists {
		// Notify the sender if the recipient does not exist
		senderConn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("User '%s' not found.", recipient)))
		return
	}

	// Format the message for both sender and recipient
	privateMessage := fmt.Sprintf("[Private] %s -> %s: %s", sender, recipient, message)

	// Send the private message to the recipient
	recipientConn.WriteMessage(websocket.TextMessage, []byte(privateMessage))

	// Also send the private message back to the sender
	senderConn.WriteMessage(websocket.TextMessage, []byte(privateMessage))
}

func broadcastMessage(message, sender string) {
	mu.Lock()
	defer mu.Unlock()
	for username, conn := range connections {
		if username != sender {
			err := conn.WriteMessage(websocket.TextMessage, []byte(message))
			if err != nil {
				logger.Printf("Error sending message to %s: %v", username, err)
				conn.Close()
				delete(connections, username)
			}
		}
	}
}

func hz(w http.ResponseWriter, r *http.Request) {
	logger.Printf("hz endpoint: %s from: %s", r.Method, r.RemoteAddr)
	_, err := w.Write([]byte("ok"))
	if err != nil {
		logger.Println("Error writing response:", err)
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return defaultValue
	}
	return value
}

func gracefulShutdown(srv *http.Server) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Fatalf("Could not gracefully shutdown the server: %v", err)
	}
	logger.Println("Graceful shutdown complete.")
}

func rateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientIP := r.RemoteAddr

		mu.Lock()
		if ipConnections[clientIP] >= maxConnectionsPerIP {
			mu.Unlock()
			http.Error(w, "Too many connections from this IP address", http.StatusTooManyRequests)
			logger.Printf("Too many connections from this IP address: %s.", clientIP)
			return
		}
		ipConnections[clientIP]++
		mu.Unlock()

		defer func() {
			mu.Lock()
			ipConnections[clientIP]--
			mu.Unlock()
		}()

		next.ServeHTTP(w, r)
	}
}

func countConnectionsByUsername(username string) int {
	count := 0
	for user := range connections {
		if user == username {
			count++
		}
	}
	return count
}

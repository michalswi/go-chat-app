package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},

	// Restrict Origins for security (avoid '# CORS')
	// CheckOrigin: func(r *http.Request) bool {
	// 	allowedOrigins := []string{"http://localhost", "https://yourdomain.com"}
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

// todo
var redisClient *redis.Client
var ctx = context.Background()

func main() {
	port := getEnv("SERVER_PORT", "443")
	certFile := getEnv("CERT", "cert.crt")
	keyFile := getEnv("KEY", "cert.key")
	redisAddr := getEnv("REDIS_ADDR", "")
	redisPass := getEnv("REDIS_PASS", "")

	if redisAddr != "" {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: redisPass,
			DB:       0,
		})
		_, err := redisClient.Ping(ctx).Result()
		if err != nil {
			logger.Fatalf("Failed to connect to Redis: %v", err)
		}
		logger.Println("Connected to Redis")
	}

	http.HandleFunc("/hc", hc)
	http.HandleFunc("/ws", handleConnections)

	logger.Println("Server is ready to handle requests at port", port)
	err := http.ListenAndServeTLS(":"+port, certFile, keyFile, nil)
	if err != nil {
		logger.Fatalf("Failed to start TLS server: %v", err)
	}
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

func hc(w http.ResponseWriter, r *http.Request) {
	logger.Printf("hc endpoint: %s", r.Method)
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

package main

import (
	"context"
	"log"
	"net/http"
	"math/rand"
	"time"
	"strconv"
	"sync"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

type Card struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

// Example cards (deck)
var cards = []Card{
	{"Cat", "😼"},
	{"Defuse", "🙅‍♂️"},
	{"Shuffle", "🔀"},
	{"Exploding Kitten", "💣"},
}

var ctx = context.Background()

type User struct {
	Username string `json:"username"`
}

var rdb *redis.Client
var upgrader = websocket.Upgrader{
    ReadBufferSize:  1024,
    WriteBufferSize: 1024,
    CheckOrigin:     func(r *http.Request) bool { return true }, // Allow all origins
}

func main() {
	log.Println("Starting server...")

	// Setup Redis
	rdb = redis.NewClient(&redis.Options{
		Addr: "localhost:6379", // Redis server address
		DB:   0,                // Use default DB
	})
	log.Println("Connected to Redis")

	// Setup Gin router
	router := gin.Default()

	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:3000"}, // Frontend origin
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
	}))

	// Routes
	router.POST("/start-game", startGame)
	router.POST("/draw-card", drawCard)

	// WebSocket for real-time updates
	router.GET("/ws", serveWs)

	// Run server
	log.Println("Running server on localhost:8080")
	router.Run("localhost:8080")
}

// Initialize a deck for the user
func initializeDeck(userID string) error {
	deckKey := "deck:" + userID

	log.Printf("Initializing deck for user: %s", userID)

	// Shuffle and add cards to the deck
	rand.Seed(time.Now().UnixNano())
	shuffledDeck := []string{
		"Cat", "Cat", "Defuse", "Shuffle", "Exploding Kitten", // Example deck of 5 cards
	}

	// Shuffle the deck (optional)
	rand.Shuffle(len(shuffledDeck), func(i, j int) {
		shuffledDeck[i], shuffledDeck[j] = shuffledDeck[j], shuffledDeck[i]
	})

	log.Printf("Shuffled deck for user: %s", userID)

	// Store the entire deck in Redis in one command
	err := rdb.RPush(ctx, deckKey, shuffledDeck).Err()
	if err != nil {
		log.Printf("Error initializing deck for user %s: %v", userID, err)
		return err
	}

	log.Printf("Deck initialized for user: %s", userID)
	return nil
}

// Start game route
func startGame(c *gin.Context) {
	var user User
	if err := c.BindJSON(&user); err != nil {
		log.Printf("Error parsing request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	log.Printf("Starting game for user: %s", user.Username)

	// Redis key for the user's deck
	deckKey := "deck:" + user.Username

	// Check if a deck already exists for this user
	existingDeck, err := rdb.LRange(ctx, deckKey, 0, -1).Result()
	if err != nil && err != redis.Nil { // Check for Redis errors, ignore if it's a 'nil' error (key doesn't exist)
		log.Printf("Error checking existing deck for user %s: %v", user.Username, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error checking existing deck"})
		return
	}

	// If a deck exists, return the existing deck
	if len(existingDeck) > 0 {
		log.Printf("Resuming game for user: %s", user.Username)
		c.JSON(http.StatusOK, gin.H{
			"message":  "Resuming game",
			"username": user.Username,
			"deck":     existingDeck,
		})
		return
	}

	// If no deck exists, initialize a new one
	err = initializeDeck(user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error initializing deck"})
		return
	}

	// Retrieve the newly initialized deck from Redis
	newDeck, err := rdb.LRange(ctx, deckKey, 0, -1).Result()
	if err != nil {
		log.Printf("Error retrieving new deck for user %s: %v", user.Username, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error retrieving new deck"})
		return
	}

	rdb.HSet(ctx, "win", user.Username, 0);
	rdb.HSet(ctx, "lose", user.Username, 0);

	log.Printf("Game started for user: %s", user.Username)
	c.JSON(http.StatusOK, gin.H{
		"message":  "Game started",
		"username": user.Username,
		"deck":     newDeck,
	})
}

func drawCard(c *gin.Context) {
	var user User
	if err := c.BindJSON(&user); err != nil {
		log.Printf("Error parsing request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	log.Printf("User %s is drawing a card", user.Username)

	// Retrieve the deck for the user from Redis
	deckKey := "deck:" + user.Username
	deck, err := rdb.LRange(ctx, deckKey, 0, -1).Result()
	if err != nil {
		log.Printf("Error retrieving deck for user %s: %v", user.Username, err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Error retrieving deck"})
		return
	}

	if len(deck) == 0 {
		updateUserStats(user.Username , true);

		log.Printf("No cards left in the deck for user: %s", user.Username)
		c.JSON(http.StatusBadRequest, gin.H{"message": "No cards left in the deck"})
		return
	}	

	// Randomly select a card index
	rand.Seed(time.Now().UnixNano())
	cardIndex := rand.Intn(len(deck))
	drawnCard := deck[cardIndex]

	log.Printf("User %s drew card: %s", user.Username, drawnCard)

	// Remove the drawn card from the Redis list
	_, err = rdb.LRem(ctx, deckKey, 1, drawnCard).Result()
	if err != nil {
		log.Printf("Error removing card from deck for user %s: %v", user.Username, err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Error removing card from deck"})
		return
	}

	// Call the function to handle the drawn card
	handleDrawnCard(c, drawnCard, user.Username)
}

func updateUserStats(username string, isWin bool) {
    // Initialize winCount and loseCount
    var winCount, loseCount int

    if isWin {
        // Retrieve the current win count for the user
        winStr, err := rdb.HGet(ctx, "win", username).Result()
        if err != nil {
            if err == redis.Nil {
                winCount = 0 // No wins recorded yet
            } else {
                log.Printf("Error retrieving win count for user %s: %v", username, err)
                return // Exit on error
            }
        } else {
            winCount, err = strconv.Atoi(winStr)
            if err != nil {
                log.Printf("Error converting win count to integer for user %s: %v", username, err)
                winCount = 0 // Default to 0 on conversion error
            }
        }

        // Increment the win count by 1 for the current match
        newWinCount := winCount + 1

		log.Printf("Count Username : %s" , username)
		log.Printf("Previous : " , winCount , "Now : " , newWinCount)
        if err := rdb.HSet(ctx, "win", username, newWinCount).Err(); err != nil {
            log.Printf("Error updating win count for user %s: %v", username, err)
            return // Exit on error
        }
        log.Printf("User %s has now won %d times", username, newWinCount)

    } else {
        // Fetch the current lose count for the user
    loseStr, err := rdb.HGet(ctx, "lose", username).Result()
    log.Printf("Loadstr for user %s: %s", username, loseStr)

    if err != nil {
        if err == redis.Nil {
            log.Printf("User %s not found in 'lose' hash.", username)
            loseCount = 0 // User not found, default to 0
        } else {
            log.Printf("Error retrieving lose count for user %s: %v", username, err)
            return // Exit on error
        }
    } else {
        // Successfully retrieved loseStr, convert to int
        loseCount, err = strconv.Atoi(loseStr)
        if err != nil {
            log.Printf("Error converting lose count to integer for user %s: %v", username, err)
            loseCount = 0 // Default to 0 on conversion error
        }
    }

    // Increment the lose count by 1 for the current match
    newLoseCount := loseCount + 1 // Increment the count
    log.Printf("Count for Username %s: %d", username, loseCount)
    log.Printf("Previous : %d Now : %d", loseCount, newLoseCount)

    // Update the lose count in Redis
    if err := rdb.HSet(ctx, "lose", username, newLoseCount).Err(); err != nil {
        log.Printf("Error updating lose count for user %s: %v", username, err)
        return // Exit on error
    }
    log.Printf("User %s has now lost %d times", username, newLoseCount)

    }
}

func handleDrawnCard(c *gin.Context, drawnCard string, username string) {
	var emoji string
	var cardType string

	// Find the emoji and card type based on the drawn card
	for _, card := range cards {
		if card.Type == drawnCard {
			cardType = card.Type
			emoji = card.Emoji
			break
		}
	}

	log.Printf("Handling card for user %s: %s (%s)", username, cardType, emoji)

	switch cardType {
	case "Exploding Kitten":
		// Check if the user has a defuse card
		hasDefuse, err := rdb.HGet(ctx, "user:"+username, "defuse").Result()
		if err != nil {
			log.Printf("Error retrieving defuse status for user %s: %v", username, err)
		}

		log.Printf("Can defuse : %t" , hasDefuse)

		defuseCount, _ := strconv.Atoi(hasDefuse)
		
		if defuseCount > 0 {  // If user has defuse card
			log.Printf("User %s used a Defuse card to defuse the Exploding Kitten!", username)
			
			// Set the defuse status to false in Redis
			rdb.HSet(ctx, "user:"+username, "defuse" , 0)
	
			// Send a response back to the user confirming they defused the bomb
			c.JSON(http.StatusOK, gin.H{"message": "You defused the Exploding Kitten using your Defuse card!", "card": emoji})
			return
		}
	
		log.Printf("User %s drew an Exploding Kitten without a Defuse card!", username)
		c.JSON(http.StatusOK, gin.H{"message": "You drew an Exploding Kitten! You lose!", "card": emoji})

		updateUserStats(username , false)
	rdb.HSet(ctx, "defuse:"+username, "false")
		return
	
	case "Defuse":
		log.Printf("User %s drew a Defuse card", username)
		c.JSON(http.StatusOK, gin.H{"message": "You drew a Defuse card! Keep this to defuse an Exploding Kitten.", "card": emoji})
		
		// Save defuse card status in Redis for future use
		rdb.HSet(ctx, "user:"+username, "defuse", 1)
		return
	

	case "Shuffle":
		log.Printf("User %s drew a Shuffle card", username)
		resetGame(username)
		c.JSON(http.StatusOK, gin.H{"message": "You drew a Shuffle card! The deck is reshuffled.", "card": emoji})
		return

	default:
		log.Printf("User %s drew a Cat card", username)
	
		// Log successful removal of the cat card
		c.JSON(http.StatusOK, gin.H{
			"message": "You drew a Cat card! One Cat card has been removed from your deck.",
			"card":    emoji,
		})
		return		
	}
}

func resetGame(username string) {
	log.Printf("Resetting game for user: %s", username)

	// Initialize deck
	deck := []string{"Cat", "Cat", "Defuse", "Shuffle", "Exploding Kitten",}

	// Shuffle the deck
	rand.Seed(time.Now().UnixNano()) // Ensure randomness on each run
	rand.Shuffle(len(deck), func(i, j int) {
		deck[i], deck[j] = deck[j], deck[i]
	})

	// Select the first 5 cards from the shuffled deck
	randomCards := deck[:5]

	// Create deck key for the user
	deckKey := "deck:" + username

	// Clear the previous deck in Redis
	rdb.Del(ctx, deckKey)

	// Add the random cards to the deck in Redis
	rdb.RPush(ctx, deckKey, randomCards)
	rdb.HSet(ctx, "user:"+username, "defuse" , 0)

	log.Printf("Game reset for user: %s with cards: %v", username, randomCards)
}

// Active WebSocket connections
var clients = make(map[*websocket.Conn]bool)
var mutex = &sync.Mutex{} // Lock to synchronize access to clients map

// Serve WebSocket connection for leaderboard
func serveWs(c *gin.Context) {
    conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
    if err != nil {
        log.Println("WebSocket upgrade failed:", err)
        return
    }

    // Register new connection
    mutex.Lock()
    clients[conn] = true
    mutex.Unlock()
    defer func() {
        mutex.Lock()
        delete(clients, conn)
        mutex.Unlock()
        conn.Close()
    }()

    log.Println("WebSocket connection established")

    // Send initial leaderboard data to the new connection
    if err := sendLeaderboard(conn); err != nil {
        log.Println("Error sending initial leaderboard data:", err)
        return
    }

    // Ping periodically to keep the connection alive
    go func() {
        ticker := time.NewTicker(30 * time.Second) // Ping every 30 seconds
        defer ticker.Stop()
        for {
            <-ticker.C
            if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                log.Println("Ping failed:", err)
                return
            }
        }
    }()

    // Keep connection alive
    for {
        if _, _, err := conn.ReadMessage(); err != nil {
            log.Println("WebSocket connection closed:", err)
            break
        }
    }
}

// Broadcast updated leaderboard to all clients
func broadcastLeaderboard() {
    // Fetch updated leaderboard data
    leaderboardData, err := fetchAllUserStats()
    if err != nil {
        log.Println("Error fetching leaderboard data:", err)
        return
    }

    // Send updated leaderboard to each connected client
    mutex.Lock()
    for conn := range clients {
        if err := conn.WriteJSON(leaderboardData); err != nil {
            log.Println("Error sending leaderboard to a client:", err)
            conn.Close()
            delete(clients, conn) // Remove client on error
        }
    }
    mutex.Unlock()
}

// Helper function to send leaderboard data to a single connection
func sendLeaderboard(conn *websocket.Conn) error {
    leaderboardData, err := fetchAllUserStats()
    if err != nil {
        return err
    }
    return conn.WriteJSON(leaderboardData)
}


// Helper function to fetch all users' data from Redis
func fetchAllUserStats() ([]map[string]string, error) {
	// Fetch all user win data
	winData, err := rdb.HGetAll(ctx, "win").Result()
	if err != nil {
		log.Printf("Error fetching win data: %v", err)
		return nil, err
	}

	// Fetch all user lose data
	loseData, err := rdb.HGetAll(ctx, "lose").Result()
	if err != nil {
		log.Printf("Error fetching lose data: %v", err)
		return nil, err
	}

	// Combine win and lose data into a single slice of maps
	var userStats []map[string]string
	for username, wins := range winData {
		// Find corresponding lose count, default to "0" if not found
		loses, ok := loseData[username]
		if !ok {
			loses = "0"
		}

		// Add each user’s stats to the list
		stats := map[string]string{
			"username": username,
			"win":      wins,
			"lose":     loses,
		}
		userStats = append(userStats, stats)
	}

	log.Println("Fetched user stats:", userStats)
	return userStats, nil
}

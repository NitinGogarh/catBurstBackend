package main

import (
	"context"
	"log"
	"net/http"
	"math/rand"
	"time"

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
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins (be cautious with this in production)
		return true
	},
}

func main() {
	// Setup Redis
	rdb = redis.NewClient(&redis.Options{
		Addr: "localhost:6379", // Redis server address
		DB:   0,                // Use default DB
	})

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
	router.GET("/leaderboard", getLeaderboard)

	// WebSocket for real-time updates
	router.GET("/ws", serveWs)

	// Run server
	router.Run("localhost:8080")
}

// Initialize a deck for the user
func initializeDeck(userID string) error {
    deckKey := "deck:" + userID

    // Shuffle and add cards to the deck
    rand.Seed(time.Now().UnixNano())
    shuffledDeck := []string{
        "Cat", "Cat", "Defuse", "Shuffle", "Exploding Kitten", // Example deck of 5 cards
    }

    // Shuffle the deck (optional)
    rand.Shuffle(len(shuffledDeck), func(i, j int) {
        shuffledDeck[i], shuffledDeck[j] = shuffledDeck[j], shuffledDeck[i]
    })

    // Store the entire deck in Redis in one command
    err := rdb.RPush(ctx, deckKey, shuffledDeck).Err()
    if err != nil {
        return err
    }

    return nil
}


// Start game route
func startGame(c *gin.Context) {
    var user User
    if err := c.BindJSON(&user); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
        return
    }

    // Redis key for the user's deck
    deckKey := "deck:" + user.Username

    // Check if a deck already exists for this user
    existingDeck, err := rdb.LRange(ctx, deckKey, 0, -1).Result()
    if err != nil && err != redis.Nil { // Check for Redis errors, ignore if it's a 'nil' error (key doesn't exist)
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Error checking existing deck"})
        return
    }

    // If a deck exists, return the existing deck
    if len(existingDeck) > 0 {
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
        c.JSON(http.StatusInternalServerError, gin.H{"error": "Error retrieving new deck"})
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "message":  "Game started",
        "username": user.Username,
        "deck":     newDeck,
    })
}

func drawCard(c *gin.Context) {
	var user User
	if err := c.BindJSON(&user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	// Retrieve the deck for the user from Redis
	deckKey := "deck:" + user.Username
	deck, err := rdb.LRange(ctx, deckKey, 0, -1).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Error retrieving deck"})
		return
	}

	if len(deck) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "No cards left in the deck"})
		return
	}

	// Randomly select a card index
	rand.Seed(time.Now().UnixNano())
	cardIndex := rand.Intn(len(deck))
	drawnCard := deck[cardIndex]

	// Remove the drawn card from the Redis list
	_, err = rdb.LRem(ctx, deckKey, 1, drawnCard).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Error removing card from deck"})
		return
	}

	// Call the function to handle the drawn card
	handleDrawnCard(c, drawnCard, user.Username)
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

	switch cardType {
	case "Exploding Kitten":
		// Player loses the game
		c.JSON(http.StatusOK, gin.H{"message": "You drew an Exploding Kitten! You lose!", "card": emoji})
		// Optional: Reset the game or end it
		resetGame(username)
		return

	case "Defuse":
		// Player drew a Defuse card; allow defusing a future bomb
		c.JSON(http.StatusOK, gin.H{"message": "You drew a Defuse card! Keep this to defuse an Exploding Kitten.", "card": emoji})
		// Save defuse card status in Redis for future use
		rdb.HSet(ctx, "user:"+username, "defuse", 1)
		return

	case "Shuffle":
		// Shuffle the deck and reset the game
		resetGame(username)
		c.JSON(http.StatusOK, gin.H{"message": "You drew a Shuffle card! The deck is reshuffled.", "card": emoji})
		return

	default:
		// Handle drawing a Cat card
		c.JSON(http.StatusOK, gin.H{"message": "You drew a Cat card!", "card": emoji})
		return
	}
}

func resetGame(username string) {
	// Example reset logic: Refill the deck and save to Redis
	deck := []string{"Cat", "Cat", "Defuse", "Shuffle", "Exploding Kitten"}
	deckKey := "deck:" + username

	// Clear the previous deck
	rdb.Del(ctx, deckKey)

	// Refill the deck
	rdb.RPush(ctx, deckKey, deck)
}


// Leaderboard route
func getLeaderboard(c *gin.Context) {
	// Fetch sorted leaderboard from Redis
	// Example: Fetch users with most wins
	c.JSON(http.StatusOK, gin.H{"leaderboard": "Top players"})
}

// WebSocket handler for real-time updates
func serveWs(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}
	defer conn.Close()

	for {
		// Handle WebSocket messages here
	}
}

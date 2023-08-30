package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/log"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Customer struct {
	Id              string    `json:"id"`
	ExternalRefList []string  `json:"external_ref_list"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (i Customer) MarshalBinary() (data []byte, err error) {
	bytes, err := json.Marshal(i)
	return bytes, err
}

func handleNewMessage(ctx context.Context, redisClient redis.UniversalClient, streamName string, consumersGroupName string, messageID string, messageData map[string]interface{}, postgresClient *pgxpool.Pool) error {
	log.Debug(fmt.Sprintf("Handling new messageID: %s data %s\n", messageID, messageData))
	// convert messageData to Customer struct
	var customer Customer
	customer.Id = messageData["id"].(string)
	// convert external_ref_list to []string by spliting the string with "," as delimiter
	customer.ExternalRefList = strings.Split(messageData["external_ref_list"].(string), ",")
	customer.FirstName = messageData["first_name"].(string)
	// check if last_name is present in the messageData
	_, ok := messageData["last_name"]
	if ok {
		customer.LastName = messageData["last_name"].(string)
	} else {
		customer.LastName = ""
	}
	// convert messageData["created_at"] string to int
	createdAt, _ := strconv.Atoi(messageData["created_at"].(string))
	// convert createdAt to time.Time, createdAt is in milliseconds
	customer.CreatedAt = time.Unix(0, int64(createdAt)*int64(time.Millisecond))
	// check if updated_at is present in the messageData
	_, ok = messageData["updated_at"]
	if ok {
		updatedAt, _ := strconv.Atoi(messageData["updated_at"].(string))
		customer.UpdatedAt = time.Unix(0, int64(updatedAt)*int64(time.Millisecond))
	} else {
		customer.UpdatedAt = customer.CreatedAt
	}
	// save the customer to postgres
	_, err := postgresClient.Exec(ctx,
		`INSERT INTO public.customer as EXISTING 
			(id, external_ref_list, first_name, last_name, created_at, updated_at) 
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO 
			UPDATE SET (external_ref_list, first_name, last_name, created_at, updated_at) = ($2, $3, $4, $5, $6)
			WHERE 
				EXISTING.id = $1 AND (
					(EXCLUDED.created_at IS NOT NULL AND EXISTING.created_at IS NULL) OR 
					(EXCLUDED.updated_at IS NOT NULL AND (EXISTING.updated_at IS NULL OR EXISTING.updated_at < EXCLUDED.updated_at))
				)
		;`,
		customer.Id, customer.ExternalRefList, customer.FirstName, customer.LastName, customer.CreatedAt, customer.UpdatedAt)
	if err != nil {
		log.Error(fmt.Sprintf("Failed to save customer.id=%s to database\n", customer.Id))
		return err
	}
	log.Info(fmt.Sprintf("customer.id=%s is succesfuly saved to database\n", customer.Id))

	// send request to lambda function url
	// this is to simulate some form of heavy workload after DB insert

	payload := []map[string]interface{}{
		{
			"ticker":    "AAPL",
			"price":     300.00,
			"timestamp": 1640995200000,
		},
		{
			"ticker":    "GOOG",
			"price":     200.00,
			"timestamp": 1640995200000,
		},
		{
			"ticker":    "META",
			"price":     100.00,
			"timestamp": 1640995200000,
		},
	}
	// randomly add data to above payload up to 15000 or 125000 (gzip on) records
	// lambda has a limitation of 6MB payload and 6MB reponse size
	// even with gzip, there is a cap for how big this payload can be
	// with timestamp is increased by 1 day each row and price is randomly increased by 0,10
	for i := 0; i < 1250; i++ {
		payload = append(payload, map[string]interface{}{
			"ticker":    "AAPL",
			"price":     payload[i*3]["price"].(float64) - 4 + rand.Float64()*10,
			"timestamp": 1640995200000 + (i+1)*86400000,
		})
		payload = append(payload, map[string]interface{}{
			"ticker":    "GOOG",
			"price":     payload[i*3+1]["price"].(float64) - 2 + rand.Float64()*5,
			"timestamp": 1640995200000 + (i+1)*86400000,
		})
		payload = append(payload, map[string]interface{}{
			"ticker":    "META",
			"price":     payload[i*3+2]["price"].(float64) - 0.5 + rand.Float64()*2,
			"timestamp": 1640995200000 + (i+1)*86400000,
		})
	}

	appLambdaUrl := os.Getenv("APP_LAMBDA_URL")
	requestBody := map[string]interface{}{
		"payload": payload,
	}

	response, err := FiberJsonRequest("POST", appLambdaUrl, requestBody, RequestOptions{
		SendGzip:    false,
		ReceiveGzip: false,
	})
	if err != nil {
		log.Error(err)
	} else {
		log.Info(response.ResonseBody["ticker_avg_price"])
	}
	// ack the message
	return redisClient.XAck(ctx, streamName, consumersGroupName, messageID).Err()
}

type RedisStreamMessage struct {
	MessageId          string
	MessageData        map[string]interface{}
	StreamName         string
	ConsumersGroupName string
}

// write a setup function to setup the handler for above message type using channel
func SetupMessageHandler(ctx context.Context, concurencyLevel int, redisClient redis.UniversalClient, postgresClient *pgxpool.Pool, responseTimeChan chan int64) chan RedisStreamMessage {
	// create a channel for RedisStreamMessage
	redisStreamMessageChan := make(chan RedisStreamMessage, concurencyLevel)
	// create a goroutine to handle the message
	go func() {
		// create a for loop to keep reading from the channel
		for {
			select {
			case <-ctx.Done():
				// if ctx is cancelled, exit the goroutine
				return
			case redisStreamMessage := <-redisStreamMessageChan:
				// mark start time in epoch nanoseconds
				startTime := time.Now().UnixNano()
				// call handleNewMessage function
				err := handleNewMessage(ctx, redisClient, redisStreamMessage.StreamName, redisStreamMessage.ConsumersGroupName, redisStreamMessage.MessageId, redisStreamMessage.MessageData, postgresClient)
				if err != nil {
					log.Error(err)
					// handle error such as retry ack
				}
				// send the total time taken to process the message to the channel
				responseTimeChan <- time.Now().UnixNano() - startTime
			}
		}
	}()
	// return the channel
	return redisStreamMessageChan
}

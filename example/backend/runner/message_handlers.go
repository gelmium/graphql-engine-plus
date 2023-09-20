package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/gofiber/fiber/v2/log"
	"github.com/jackc/pgx/v5/pgxpool"
	jsoniter "github.com/json-iterator/go"
	"github.com/redis/go-redis/v9"
)

var ConfigFastest = jsoniter.Config{
	EscapeHTML:                    false,
	MarshalFloatWith6Digits:       true,
	ObjectFieldMustBeSimpleString: true,
	TagKey:                        "json",
}.Froze()

type Customer struct {
	Id              string    `json:"id"`
	ExternalRefList []string  `json:"external_ref_list"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

func (c Customer) MarshalBinary() (data []byte, err error) {
	bytes, err := jsoniter.Marshal(c)
	return bytes, err
}

func handleNewMessage(ctx context.Context, redisClient redis.UniversalClient, streamName string, consumersGroupName string, messageID string, messageData map[string]interface{}, postgresClient *pgxpool.Pool) error {
	log.Info("Start handling messageID:", messageID)
	// convert messageData to Customer struct
	var customer Customer
	ConfigFastest.UnmarshalFromString(messageData["payload"].(string), &customer)
	if customer.UpdatedAt.IsZero() {
		// set to now
		customer.UpdatedAt = time.Now()
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

// write a setup function to setup the handler for above message type using channel
func SetupMessageHandler(ctx context.Context, concurencyLevel int, redisClient redis.UniversalClient, postgresClient *pgxpool.Pool, responseTimeChan chan int64) chan RedisStreamMessage {
	// create a channel for RedisStreamMessage
	redisStreamMessageChan := make(chan RedisStreamMessage, concurencyLevel)
	// create multiples goroutine to handle the message
	// number of goroutine is equal to concurencyLevel
	for i := 0; i < concurencyLevel; i++ {
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
	}
	// return the channel
	return redisStreamMessageChan
}

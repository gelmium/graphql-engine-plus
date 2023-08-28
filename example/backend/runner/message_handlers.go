package main

import (
	"context"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/log"
	"github.com/redis/go-redis/v9"
)

func handleNewMessage(ctx context.Context, redisClient redis.UniversalClient, streamName string, consumersGroupName string, messageID string, messageData map[string]interface{}) error {
	// log.Info("Handling new messageID: %s data %s\n", messageID, messageData)

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
	customer.CreatedAt, _ = strconv.Atoi(messageData["created_at"].(string))
	// check if updated_at is present in the messageData
	_, ok = messageData["updated_at"]
	if ok {
		customer.UpdatedAt, _ = strconv.Atoi(messageData["updated_at"].(string))
	} else {
		customer.UpdatedAt = 0
	}
	// save the customer to redis using the messageID as the key with SET command
	key := "data:public.customer:" + customer.Id
	err := redisClient.Set(ctx, key, customer, 3600*time.Second).Err()
	if err != nil {
		log.Error("Error saving customer.id=%s: %s\n", customer.Id, err)
		return err
	}
	// send request to lambda function url
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
	for i := 0; i < 12500; i++ {
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
	log.Info(response.ResonseBody["ticker_avg_price"])
	// ack the message
	return redisClient.XAck(ctx, streamName, consumersGroupName, messageID).Err()
}

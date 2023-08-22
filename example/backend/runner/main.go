package main

// import fiber library
import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/redis/go-redis/v9"
	"github.com/rs/xid"
)

// setup a fiber app which contain a simple /health endpoint which return a 200 status code

func Setup() *fiber.App {
	app := fiber.New()
	app.Use(logger.New(logger.Config{
		Format:     "${time} - ${method} ${path} ${status} - ${latency} [${ip}:${port}]\n",
		TimeFormat: "2006/01/02 15:04:05.000000",
	}))
	app.Get("/health", func(c *fiber.Ctx) error {
		// read GET parameter from sleep from url localhost:3000/health?sleep=30
		sleep := c.Query("sleep")
		// ParseInt string sleep to int
		sleepDuration, err := strconv.ParseInt(sleep, 10, 64)
		// sleep for the given time, sleepDuration is in milliseconds
		if err == nil && sleepDuration > 0 {
			// sleep max 120 seconds
			time.Sleep(time.Duration(min(sleepDuration, 120*1000)) * time.Millisecond)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	// add a POST endpoint to forward request to an upstream url
	app.Post("/v1/graphql", func(c *fiber.Ctx) error {
		// fire a POST request to the upstream url using the same header and body from the original request
		agent := fiber.Post("http://localhost:8880/v1/graphql")

		// loop through the header and set the header from the original request
		c.Request().Header.VisitAll(func(key, value []byte) {
			agent.Request().Header.SetBytesKV(key, value)
		})
		// set the body from the original request
		agent.Request().SetBody(c.Request().Body())
		// send the request to the upstream url using Fiber Go
		if err := agent.Parse(); err != nil {
			panic(err)
		}
		code, body, errs := agent.Bytes()
		if len(errs) > 0 {
			panic(errs)
		}

		// return the response from the upstream url
		return c.Status(code).Send(body)
	})

	return app
}

func SetupRedisWorker() {
	ctx := context.Background()
	var avgTime int64 = 500 // in milliseconds
	var totalCount int64 = 1
	// create a channel of int64
	responseTime := make(chan int64)
	appRunnerHealthUrl := os.Getenv("AWS_APP_RUNNER_HEALTH_ENDPOINT_URL")
	// opt, err := redis.ParseURL("redis://localhost:6379/0")
	// if err != nil {
	// 	panic(err)
	// }
	// redisClient := redis.NewClient(opt)
	// reading clusterUrl from environment variable
	clusterUrl := os.Getenv("REDIS_CLUSTER_URL")
	clusterOpt, err := redis.ParseClusterURL(clusterUrl)
	if err != nil {
		panic(err)
	}
	clusterOpt.TLSConfig.InsecureSkipVerify = true
	redisClient := redis.NewClusterClient(clusterOpt)
	// ping redis server
	err = redisClient.Ping(ctx).Err()
	if err != nil {
		panic(err)
	}
	log.Info("Connected to Redis server")

	stream := "audit:public.customer:insert"
	consumersGroup := "test-go-consumer-group"

	err = redisClient.XGroupCreate(ctx, stream, consumersGroup, "0").Err()
	if err != nil {
		log.Error(err)
	}
	// create stream automatically if not exist
	err = redisClient.XGroupCreateMkStream(ctx, stream, consumersGroup, "0").Err()
	if err != nil {
		log.Error(err)
	}
	uniqueID := xid.New().String()
	log.Info("Consumer is ready: " + uniqueID)
	// start main loop
	for {
		entries, err := redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    consumersGroup,
			Consumer: uniqueID,
			Streams:  []string{stream, ">"},
			Count:    10, // read 10 messages at a time
			Block:    0,
			NoAck:    false,
		}).Result()
		if err != nil {
			log.Fatal(err)
		}

		// reading from entries[0] as we are only reading from one stream
		// for reading multiple streams, we need to loop through entries
		// create sync.WaitGroup
		var wg sync.WaitGroup
		msgCount := len(entries[0].Messages)
		log.Info("Received msg: " + strconv.Itoa(msgCount))
		for i := 0; i < msgCount; i++ {
			messageID := entries[0].Messages[i].ID
			messageData := entries[0].Messages[i].Values
			wg.Add(1)
			go func() {
				// mark start time in epoch milliseconds
				startTime := time.Now().UnixMilli()
				defer wg.Done()
				err := handleNewMessage(ctx, redisClient, messageID, messageData)
				if err != nil {
					redisClient.XAck(ctx, entries[0].Stream, consumersGroup, messageID)
				}
				// send the total time taken to process the message to the channel
				responseTime <- time.Now().UnixMilli() - startTime
			}()
		}
		// use app runner health url to simulate request latency to AWS App Runner service
		// we fire the request first using avgTime as the sleep time
		// before waiting for the goroutines to finish
		for i := 0; i < msgCount; i++ {
			go func() {
				// fire GET request to app runner health url using fiber.GET
				agent := fiber.Get(appRunnerHealthUrl)
				// set Get QueryString
				agent.QueryString("sleep=" + strconv.FormatInt(avgTime, 10))
				// send the request to the upstream url using Fiber Go
				if err := agent.Parse(); err != nil {
					log.Error(err)
				}
				_, _, errs := agent.Bytes()
				if len(errs) > 0 {
					log.Error(errs)
				}
			}()
		}
		// wait for the response time from the channel and
		// recalculate the average response time
		for i := 0; i < msgCount; i++ {
			// wait for the response time from the channel
			avgTime = (avgTime*totalCount + (<-responseTime)) / (totalCount + 1)
			totalCount++
		}
		// wait for all handleNewMessage goroutines to finish (to be sure that all messages are processed)
		// other gorooutines such as the one that fire request to app runner health url will continue to run
		wg.Wait()
	}
	// end main loop
}

type Customer struct {
	Id              string   `json:"id"`
	ExternalRefList []string `json:"external_ref_list"`
	FirstName       string   `json:"first_name"`
	LastName        string   `json:"last_name"`
	CreatedAt       int      `json:"created_at"`
	UpdatedAt       int      `json:"updated_at"`
}

func (i Customer) MarshalBinary() (data []byte, err error) {
	bytes, err := json.Marshal(i)
	return bytes, err
}

func handleNewMessage(ctx context.Context, redisClient redis.UniversalClient, messageID string, messageData map[string]interface{}) error {
	// log.Info("Handling new messageID: %s data %s\n", messageID, messageData)

	// convert messageData to Customer struct
	var customer Customer
	customer.Id = messageData["id"].(string)
	// convert external_ref_list to []string by spliting the string with "," as delimiter
	customer.ExternalRefList = strings.Split(messageData["external_ref_list"].(string), ",")
	customer.FirstName = messageData["first_name"].(string)
	customer.LastName = messageData["last_name"].(string)
	// convert messageData["created_at"] string to int
	customer.CreatedAt, _ = strconv.Atoi(messageData["created_at"].(string))
	// check if updated_at is present in the messageData
	_, ok := messageData["updated_at"]
	if ok {
		customer.UpdatedAt, _ = strconv.Atoi(messageData["updated_at"].(string))
	} else {
		customer.UpdatedAt = 0
	}
	// save the customer to redis using the messageID as the key with SET command
	key := "data:public.customer:" + customer.Id
	err := redisClient.Set(ctx, key, customer, 0).Err()
	if err != nil {
		log.Error("Error saving customer.id=%s: %s\n", customer.Id, err)
		return err
	}
	return nil
}

func main() {
	app := Setup()
	go SetupRedisWorker()
	app.Listen(":3000")
}

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
	app := fiber.New(
		fiber.Config{
			ReadTimeout:  60 * time.Second,
			WriteTimeout: 60 * time.Second,
			ServerHeader: "App Runner Demo",
		},
	)
	app.Use(logger.New(logger.Config{
		Format:     "${time} \"${method} ${path}\" ${status} ${latency} ${ip}\n",
		TimeFormat: "2006/01/02 15:04:05.000000",
	}))
	app.Get("/health", func(c *fiber.Ctx) error {
		// read GET parameter from sleep from url localhost:3000/health?sleep=30
		sleep := c.Query("sleep")
		// ParseInt string sleep to int
		sleepDuration, err := strconv.ParseInt(sleep, 10, 64)
		// sleep for the given time, sleepDuration is in microseconds
		if err == nil && sleepDuration > 0 {
			// sleep max 120 seconds
			time.Sleep(time.Duration(min(sleepDuration, 120_000_000)) * time.Microsecond)
		}
		return c.SendStatus(fiber.StatusOK)
	})

	return app
}

func SetupRedisWorker(ctx context.Context, gshutdownChanel chan error) {
	var avgTime int64 = 1_000_000 // 1ms in nanoseconds unit
	var totalCount int64 = 1
	// set this flag to True will tell the redis worker to
	// fire health request before finish processing the message
	preFireHealthReq, err := strconv.ParseBool(os.Getenv("PREFIRE_HEALTH_REQUEST"))
	if err != nil {
		preFireHealthReq = false
	}
	// create a channel of int64
	responseTime := make(chan int64)
	// worker configuration
	concurencyLevel, err := strconv.ParseInt(os.Getenv("CONCURRENCY_LEVEL"), 10, 64)
	if err != nil {
		concurencyLevel = 10
	}
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

	stream := "worker:public.customer:insert"
	consumersGroup := "test-go-consumer-group"

	// create stream and consumer group automatically if not exist
	err = redisClient.XGroupCreateMkStream(ctx, stream, consumersGroup, "0").Err()
	if err != nil {
		log.Error(err)
	}

	uniqueID := xid.New().String()
	log.Info("Consumer is ready: " + uniqueID)
	// start main loop
	for {
		// check context for cancellation
		select {
		case <-ctx.Done():
			log.Info("Context cancelled, delete consumer id from consumer group")
			// delete consumer id from consumer group
			err = redisClient.XGroupDelConsumer(context.Background(), stream, consumersGroup, uniqueID).Err()
			if err != nil {
				log.Error(err)
			}
			// gracefully shutdown redis client
			log.Info("Context cancelled, close redis client")
			gshutdownChanel <- redisClient.Close()
			close(gshutdownChanel)
			return
		default:
		}

		entries, err := redisClient.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    consumersGroup,
			Consumer: uniqueID,
			Streams:  []string{stream, ">"},
			Count:    concurencyLevel,                      // read up to n messages at a time
			Block:    time.Duration(10 * time.Millisecond), // wait max 10ms each iteration
			NoAck:    false,
		}).Result()
		if err != nil {
			// check if "redis: nil" in error message
			if strings.Contains(err.Error(), "redis: nil") {
				// no new message, continue to next iteration
				continue
			}
			// else log the error
			log.Error(err)
		}

		// reading from entries[0] as we are only reading from one stream
		// for reading multiple streams, we need to loop through entries
		// create sync.WaitGroup
		msgCount := len(entries[0].Messages)
		if msgCount == 0 {
			continue
		}
		var wg sync.WaitGroup
		log.Info("Received msg: " + strconv.Itoa(msgCount))
		for i := 0; i < msgCount; i++ {
			messageID := entries[0].Messages[i].ID
			messageData := entries[0].Messages[i].Values
			wg.Add(1)
			go func() {
				defer wg.Done()
				// mark start time in epoch nanoseconds
				startTime := time.Now().UnixNano()
				// call handler
				err := handleNewMessage(ctx, redisClient, entries[0].Stream, consumersGroup, messageID, messageData)
				if err != nil {
					log.Error(err)
					// handle error such as retry ack
					// or send to dead letter queue
				}
				// send the total time taken to process the message to the channel
				responseTime <- time.Now().UnixNano() - startTime
			}()
		}
		// use app runner health url to simulate request latency to AWS App Runner service
		// we fire the request first using avgTime as the sleep time
		// before waiting for the goroutines to finish

		if preFireHealthReq {
			for i := 0; i < msgCount; i++ {
				go func() {
					// fire GET request to app runner health url using fiber.GET
					agent := fiber.Get(appRunnerHealthUrl)
					// set Get QueryString
					// sleep parameter is in microseconds
					// convert avgTime from nanoseconds to microseconds
					agent.QueryString("sleep=" + strconv.FormatInt(avgTime/1000, 10))
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
		}
		// wait for the response time from the channel
		for i := 0; i < msgCount; i++ {
			// wait for the response time from the channel
			select {
			case resTime := <-responseTime:
				if preFireHealthReq {
					// recalculate the average response time
					avgTime = (avgTime*totalCount + resTime) / (totalCount + 1)
					totalCount++
				} else {
					// post fire health request using the exact response time
					go func() {
						// fire GET request to app runner health url using fiber.GET
						agent := fiber.Get(appRunnerHealthUrl)
						agent.QueryString("sleep=" + strconv.FormatInt(resTime/1000, 10))
						if err := agent.Parse(); err != nil {
							log.Error(err)
						}
						_, _, errs := agent.Bytes()
						if len(errs) > 0 {
							log.Error(errs)
						}
					}()
				}
				break
			case <-time.After(30 * time.Second):
				log.Error("Timed out waiting for response time metric")
				// reset the response time to 1ms
				avgTime = 1_000_000
				totalCount = 1
				break
			}
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

func handleNewMessage(ctx context.Context, redisClient redis.UniversalClient, streamName string, consumersGroupName string, messageID string, messageData map[string]interface{}) error {
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
	err := redisClient.Set(ctx, key, customer, 3600*time.Second).Err()
	if err != nil {
		log.Error("Error saving customer.id=%s: %s\n", customer.Id, err)
		return err
	}
	return redisClient.XAck(ctx, streamName, consumersGroupName, messageID).Err()
}

func main() {

	app := Setup()
	workerCtx, cancelWorkerFn := context.WithCancel(context.Background())
	// define a channel to wait for graceful shutdown
	workkerGracefulShutdownChanel := make(chan error)
	go SetupRedisWorker(workerCtx, workkerGracefulShutdownChanel)
	go app.Listen(":3000")

	// wait for termination signal and register database & http server clean-up operations
	shutdownTimeout := 30 * time.Second
	wait := GracefulShutdown(context.Background(), shutdownTimeout, map[string]operation{
		"redis-worker": func(ctx context.Context) error {
			cancelWorkerFn()
			return <-workkerGracefulShutdownChanel
		},
		"http-server": func(ctx context.Context) error {
			return app.ShutdownWithTimeout(shutdownTimeout - 1*time.Second)
		},
		// Add other cleanup operations here
	})
	<-wait
	log.Info("Graceful shutdown completed")
}
